package playback

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"qmediasync/internal/helpers"
)

const (
	preloadQueueLimit = 32
	preloadStateLimit = 256
	preloadWorkers    = 2
	preloadDetailWait = 2 * time.Second
	preloadWaitLimit  = 15 * time.Second
	preloadRunLimit   = 3 * time.Second
	preloadAccountGap = 10 * time.Second
	preloadRetryWait  = 30 * time.Second
	preloadRiskWait   = 10 * time.Minute
)

var (
	ErrPreloadMetadataBudget = errors.New("预加载元数据请求额度已用尽")
	ErrPreloadCanceled       = errors.New("预加载候选已失效")
)

// PreloadKind 区分详情和可靠播放进度产生的下一项。
type PreloadKind uint8

const (
	PreloadDetail PreloadKind = iota
	PreloadNext
)

// PreloadAccount 的完整身份同时隔离提供方、本地账号和网盘用户。
type PreloadAccount struct {
	Provider  string
	AccountID uint
	UserID    string
}

// PreloadOutcome 只允许安全分类，不接收上游原文或外部错误。
type PreloadOutcome uint8

const (
	PreloadSkipped PreloadOutcome = iota
	PreloadCached
	PreloadPublished
	PreloadTransient
	PreloadRisk
	PreloadUnauthorized
	PreloadFailed
)

// PreloadResult 携带锁外日志需要的标量；RetryAfter 只延长账号冷却。
type PreloadResult struct {
	Outcome      PreloadOutcome
	Reason       string
	StatusSource string
	HTTPStatus   int
	BusinessCode int
	RetryAfter   time.Duration
}

// PreloadCandidate 由可信适配器提交不可变快照，Run 不得捕获 Gin Context。
// Key 包含认证、设备、源、模式、UA、用途及播放关联；Scope 是认证设备作用域。
// Source 与正常取链接手通知使用相同身份；Enriched 仅表示新的正常响应补齐资料。
type PreloadCandidate struct {
	Key      string
	Scope    string
	Instance string
	Source   string
	Account  PreloadAccount
	Kind     PreloadKind
	Log      PreloadLogContext
	Enriched bool
	Run      func(context.Context, *PreloadTask) PreloadResult
}

type preloadSettings struct {
	enabled    bool
	generation uint64
}

type preloadState struct {
	metadata          uint8
	metadataSucceeded uint8
	finalMetadata     bool
	attempts          uint8
	terminal          bool
	enrichable        bool
	revoked           bool
	retryAt           time.Time
	expiresAt         time.Time
	account           PreloadAccount
	source            string
	run               func(context.Context, *PreloadTask) PreloadResult
	job               *preloadJob
	candidate         PreloadCandidate
}

type preloadAccountState struct {
	nextStart time.Time
	cooldown  time.Time
}

type preloadJob struct {
	candidate  PreloadCandidate
	run        func(context.Context, *PreloadTask) PreloadResult
	state      *preloadState
	generation uint64
	sequence   uint64
	readyAt    time.Time
	expiresAt  time.Time
	task       *PreloadTask
}

// PreloadTask 将元数据额度、签名准入和发布门禁绑定到一次有限执行。
type PreloadTask struct {
	owner *Preloader
	job   *preloadJob
	ctx   context.Context
	stop  context.CancelFunc
	// gate 只保护内存发布；取消方先置 invalid，锁外等待已有发布完成。
	gate    sync.Mutex
	invalid atomic.Bool
	// 以下字段由 owner.mu 保护。
	signing        bool
	published      bool
	waitingAccount bool
	deferred       time.Time
	log            PreloadLogContext
	cancelReason   string
}

// Preloader 使用两个固定 worker 和一条有界队列，不持久化或创建逐事件 goroutine。
type Preloader struct {
	settings atomic.Pointer[preloadSettings]
	mu       sync.Mutex
	states   map[string]*preloadState
	accounts map[PreloadAccount]preloadAccountState
	// 普通播放短暂阻止同源后台重入，也覆盖尚未绑定源的准备阶段。
	takeovers map[string]time.Time
	queue     []*preloadJob
	active    []*preloadJob
	sequence  uint64
	canceled  uint64
	nextSweep time.Time
	wake      chan struct{}
	root      context.Context
	stop      context.CancelFunc
	done      chan struct{}
	started   bool
	stopped   bool
}

// DefaultPreloader 由程序生命周期启动，默认关闭。
var DefaultPreloader = NewPreloader()

// NewPreloader 创建尚未启动且默认关闭的协调器。
func NewPreloader() *Preloader {
	p := &Preloader{
		states:    make(map[string]*preloadState),
		accounts:  make(map[PreloadAccount]preloadAccountState),
		takeovers: make(map[string]time.Time),
		wake:      make(chan struct{}, 1),
	}
	p.settings.Store(&preloadSettings{})
	return p
}

// Enabled 供协议适配器在构造候选前无锁跳过关闭路径。
func (p *Preloader) Enabled() bool { return p != nil && p.settings.Load().enabled }

// Start 为整个服务生命周期启动固定 worker，重复调用无副作用。
func (p *Preloader) Start(ctx context.Context) {
	p.mu.Lock()
	if p.started || p.stopped || ctx == nil {
		p.mu.Unlock()
		return
	}
	p.root, p.stop = context.WithCancel(ctx)
	p.done = make(chan struct{})
	p.started = true
	p.mu.Unlock()
	go p.work()
}

// Apply 在持久设置成功提交后热生效，较旧的通知不能恢复旧代际。
func (p *Preloader) Apply(enabled bool, generation uint64) {
	p.mu.Lock()
	old := p.settings.Load()
	if generation < old.generation || p.stopped || (old.enabled == enabled && old.generation == generation) {
		p.mu.Unlock()
		return
	}
	p.settings.Store(&preloadSettings{enabled: enabled, generation: generation})
	tasks := p.cancelLocked(func(*preloadJob) bool { return true }, false)
	p.states = make(map[string]*preloadState)
	p.mu.Unlock()
	finishPreloadCancellation(tasks)
	p.signal()
}

// Stop 先取消并排空后台，再由调用方关闭普通播放客户端。
func (p *Preloader) Stop() {
	p.mu.Lock()
	if p.stopped {
		done := p.done
		p.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	p.stopped = true
	old := p.settings.Load()
	p.settings.Store(&preloadSettings{generation: old.generation})
	tasks := p.cancelLocked(func(*preloadJob) bool { return true }, false)
	p.states = make(map[string]*preloadState)
	p.accounts = make(map[PreloadAccount]preloadAccountState)
	p.takeovers = make(map[string]time.Time)
	stop, done := p.stop, p.done
	p.mu.Unlock()
	if stop != nil {
		stop()
	}
	finishPreloadCancellation(tasks)
	if done != nil {
		<-done
	}
}

// Submit 先在短临界区去重，再异步处理；同项补资料不重置详情防抖。
func (p *Preloader) Submit(candidate PreloadCandidate) bool {
	if !p.Enabled() || candidate.Key == "" || candidate.Scope == "" || candidate.Run == nil || candidate.Kind > PreloadNext {
		return false
	}
	candidate.Log = candidate.Log.bounded()
	now := time.Now()
	p.mu.Lock()
	settings := p.settings.Load()
	if !settings.enabled || p.stopped || !p.started {
		p.mu.Unlock()
		return false
	}
	p.sweepLocked(now, false)
	tasks := p.cancelLocked(func(job *preloadJob) bool {
		return job.candidate.Scope == candidate.Scope && job.candidate.Kind == candidate.Kind && job.candidate.Key != candidate.Key
	}, false, "candidate_changed")
	state := p.states[candidate.Key]
	if state != nil && state.job != nil && state.job.task != nil && state.job.task.invalid.Load() && state.attempts == 0 {
		state.job = nil
	}
	accepted := false
	if state != nil && state.job != nil {
		if state.job.task == nil {
			state.job.candidate = candidate
			state.job.run = candidate.Run
			state.run = candidate.Run
		}
		accepted = true
	} else if state == nil || (!state.revoked && !now.Before(state.retryAt) && (!state.terminal || (candidate.Enriched && state.enrichable && state.attempts == 0))) {
		account := candidate.Account
		if state != nil && account == (PreloadAccount{}) {
			account = state.account
		}
		cooldown := p.accounts[preloadAccountKey(account)].cooldown
		if !cooldown.After(now) && !p.takeovers[candidate.Source].After(now) && len(p.queue) < preloadQueueLimit {
			if state == nil && p.reserveStateLocked(now) {
				state = &preloadState{expiresAt: now.Add(URLCacheTTL)}
				p.states[candidate.Key] = state
			}
			if state != nil {
				if state.attempts == 0 || state.run == nil {
					state.run = candidate.Run
				}
				state.terminal = false
				state.candidate = candidate
				p.sequence++
				job := &preloadJob{candidate: candidate, run: state.run, state: state, generation: settings.generation, sequence: p.sequence, readyAt: now, expiresAt: now.Add(preloadWaitLimit)}
				if candidate.Kind == PreloadDetail {
					job.readyAt = now.Add(preloadDetailWait)
				}
				state.job = job
				p.queue = append(p.queue, job)
				accepted = true
			}
		}
	}
	p.mu.Unlock()
	finishPreloadCancellation(tasks)
	if accepted {
		p.signal()
	}
	return accepted
}

// CancelScope 用于暂停、停止或失效的当前播放关联。
func (p *Preloader) CancelScope(scope string) {
	p.cancelMatching(func(job *preloadJob) bool { return scope != "" && job.candidate.Scope == scope }, false)
}

// CancelDetail 仅撤销当前详情，不影响正在播放内容的下一项。
func (p *Preloader) CancelDetail(scope string) {
	p.cancelMatching(func(job *preloadJob) bool {
		return scope != "" && job.candidate.Scope == scope && job.candidate.Kind == PreloadDetail
	}, false)
}

// CancelNext 在进度或播放关联失效时撤销下一项，不影响当前详情。
func (p *Preloader) CancelNext(scope string) {
	p.cancelMatching(func(job *preloadJob) bool {
		return scope != "" && job.candidate.Scope == scope && job.candidate.Kind == PreloadNext
	}, false)
}

// CancelInstance 用于代理实例被替换或停止。
func (p *Preloader) CancelInstance(instance string) {
	p.cancelMatching(func(job *preloadJob) bool { return instance != "" && job.candidate.Instance == instance }, false)
}

// CancelAll 撤销来源变更前的全部作业，保留账号限额和候选已使用的重试额度。
func (p *Preloader) CancelAll() {
	p.cancelMatching(func(*preloadJob) bool { return true }, false)
}

// CancelAccount 在凭据提交成功后撤销旧作业；空 UserID 匹配该本地账号的所有旧 UID。
func (p *Preloader) CancelAccount(account PreloadAccount) {
	p.cancelMatching(func(job *preloadJob) bool {
		actual := job.state.account
		if actual == (PreloadAccount{}) {
			actual = job.candidate.Account
		}
		return actual.Provider == account.Provider && (actual.AccountID == account.AccountID || actual.AccountID == 0) && (account.UserID == "" || actual.UserID == "" || actual.UserID == account.UserID)
	}, false)
}

// TakeoverScope 只接手当前详情，保留不同目标的下一集资格。
func (p *Preloader) TakeoverScope(scope string) {
	p.cancelMatching(func(job *preloadJob) bool {
		return scope != "" && job.candidate.Scope == scope && job.candidate.Kind == PreloadDetail
	}, true)
}

// TakeoverNext 在实际切集时撤销旧后继机会，保留已经进入签名的共享 leader。
func (p *Preloader) TakeoverNext(scope string) {
	p.cancelMatching(func(job *preloadJob) bool {
		return scope != "" && job.candidate.Scope == scope && job.candidate.Kind == PreloadNext
	}, true)
}

// Takeover 立即撤销同源准备；已经签名的 leader 继续供普通播放共享。
func (p *Preloader) Takeover(source string) {
	if source == "" || !p.Enabled() {
		return
	}
	p.mu.Lock()
	now := time.Now()
	if _, exists := p.takeovers[source]; exists || p.reserveStateLocked(now) {
		p.takeovers[source] = now.Add(preloadWaitLimit)
	}
	tasks := p.cancelLocked(func(job *preloadJob) bool { return job.candidate.Source == source || job.state.source == source }, true)
	p.mu.Unlock()
	finishPreloadCancellation(tasks)
	p.signal()
}

func (p *Preloader) cancelMatching(match func(*preloadJob) bool, takeover bool) {
	p.mu.Lock()
	tasks := p.cancelLocked(match, takeover)
	p.mu.Unlock()
	finishPreloadCancellation(tasks)
	p.signal()
}

type preloadCancellation struct {
	tasks []*PreloadTask
	count int
}

func (p *Preloader) cancelLocked(match func(*preloadJob) bool, takeover bool, reasons ...string) preloadCancellation {
	var canceled preloadCancellation
	for i := 0; i < len(p.queue); {
		job := p.queue[i]
		if !match(job) {
			i++
			continue
		}
		job.state.job = nil
		job.state.revoked = job.state.attempts > 0 || takeover
		job.state.run = nil
		job.state.candidate.Run = nil
		p.queue = append(p.queue[:i], p.queue[i+1:]...)
		canceled.count++
	}
	for _, job := range p.active {
		if !match(job) {
			continue
		}
		if !job.state.revoked {
			canceled.count++
		}
		job.state.revoked = job.state.attempts > 0 || takeover
		if !takeover || !job.task.signing {
			job.task.cancelReason = "canceled"
			if takeover {
				job.task.cancelReason = "playback_started"
			} else if len(reasons) != 0 {
				job.task.cancelReason = reasons[0]
			}
			job.task.invalid.Store(true)
			canceled.tasks = append(canceled.tasks, job.task)
		}
	}
	for key, state := range p.states {
		if state.job == nil && match(&preloadJob{candidate: state.candidate, state: state}) {
			if !state.revoked && state.attempts > 0 && !state.terminal {
				canceled.count++
			}
			state.revoked = state.revoked || state.attempts > 0 || takeover
			state.run = nil
			state.candidate.Run = nil
			if !takeover && !state.terminal && state.metadata == 0 && state.attempts == 0 {
				delete(p.states, key)
			}
		}
	}
	p.canceled += uint64(canceled.count)
	return canceled
}

func finishPreloadCancellation(canceled preloadCancellation) {
	for _, task := range canceled.tasks {
		task.stop()
		task.gate.Lock()
		task.gate.Unlock()
	}
}

// Valid 不取全局锁，可在补查、拿键锁和请求前重复检查。
func (t *PreloadTask) Valid() bool {
	settings := t.owner.settings.Load()
	return !t.invalid.Load() && t.ctx.Err() == nil && settings.enabled && settings.generation == t.job.generation
}

// Metadata 在两次机会间共享最多两次 HTTP；安全 GET 仅有一次 400ms 退避重试。
// call 返回的 retryable 必须已排除授权、风控、业务拒绝和错误 schema。
func (t *PreloadTask) Metadata(ctx context.Context, safeGET bool, call func(context.Context) (retryable bool, err error)) error {
	return t.metadata(ctx, safeGET, false, call)
}

// FinalMetadataGET 仅用于已经明确的最后一层普通 GET；第三次不重试，也不补偿之前的失败。
func (t *PreloadTask) FinalMetadataGET(ctx context.Context, call func(context.Context) (retryable bool, err error)) error {
	return t.metadata(ctx, true, true, call)
}

func (t *PreloadTask) metadata(ctx context.Context, safeGET, finalGET bool, call func(context.Context) (retryable bool, err error)) error {
	for attempt := 0; ; attempt++ {
		t.owner.mu.Lock()
		if !t.Valid() || ctx.Err() != nil {
			t.owner.mu.Unlock()
			return ErrPreloadCanceled
		}
		state := t.job.state
		limit := uint8(2)
		if finalGET && attempt == 0 {
			if state.finalMetadata || state.metadata > 2 || (state.metadata == 2 && state.metadataSucceeded != 2) {
				t.owner.mu.Unlock()
				return ErrPreloadMetadataBudget
			}
			state.finalMetadata = true
			if state.metadata == 2 {
				limit = 3
			}
		}
		if state.metadata >= limit {
			t.owner.mu.Unlock()
			return ErrPreloadMetadataBudget
		}
		state.metadata++
		t.owner.mu.Unlock()
		retryable, err := call(ctx)
		if err == nil {
			t.owner.mu.Lock()
			state.metadataSucceeded++
			t.owner.mu.Unlock()
		}
		if err == nil || !safeGET || !retryable || attempt != 0 || !t.Valid() {
			return err
		}
		t.owner.mu.Lock()
		remaining := t.job.state.metadata < 2
		t.owner.mu.Unlock()
		if deadline, ok := ctx.Deadline(); !remaining || (ok && time.Until(deadline) <= 400*time.Millisecond) {
			return err
		}
		helpers.AppLogger.Debugf("预加载元数据临时失败，将在本轮额度内重试：任务=%d", t.job.sequence)
		timer := time.NewTimer(400 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-t.ctx.Done():
			timer.Stop()
			return ErrPreloadCanceled
		case <-timer.C:
		}
	}
}

// BindSource 在复核真实账号后绑定；同一作业不能切换提供方身份。
func (t *PreloadTask) BindSource(account PreloadAccount, source string) bool {
	t.owner.mu.Lock()
	bound := t.bindSourceLocked(account, source)
	t.owner.mu.Unlock()
	t.owner.signal()
	return bound
}

func (t *PreloadTask) bindSourceLocked(account PreloadAccount, source string) bool {
	if !t.Valid() || source == "" || account.Provider == "" || (account.AccountID != 0 && account.UserID == "") || (t.job.candidate.Source != "" && t.job.candidate.Source != source) || t.owner.takeovers[source].After(time.Now()) {
		return false
	}
	state := t.job.state
	previous := preloadJobAccount(t.job)
	if (previous.Provider != "" && previous.Provider != account.Provider) ||
		(previous.UserID != "" && account.UserID != "" && previous.UserID != account.UserID) ||
		(previous.AccountID != 0 && account.AccountID != 0 && previous.AccountID != account.AccountID) ||
		(state.source != "" && state.source != source) {
		return false
	}
	if account.AccountID == 0 {
		account.AccountID = previous.AccountID
	}
	if account.UserID == "" {
		account.UserID = previous.UserID
	}
	state.account, state.source = account, source
	if !t.owner.accountAvailableLocked(t.job) {
		t.waitingAccount = true
		return false
	}
	t.waitingAccount = false
	return !t.owner.accounts[preloadAccountKey(account)].cooldown.After(time.Now())
}

// BeginSignature 必须紧邻唯一提供方请求；延迟只回到原队列，不占住 worker。
func (t *PreloadTask) BeginSignature(account PreloadAccount, source string) bool {
	p := t.owner
	p.mu.Lock()
	defer func() {
		p.mu.Unlock()
		p.signal()
	}()
	if account.AccountID == 0 || t.signing || !t.bindSourceLocked(account, source) || t.job.state.revoked || t.job.state.attempts >= 2 {
		return false
	}
	now := time.Now()
	key := preloadAccountKey(account)
	state, exists := p.accounts[key]
	if state.nextStart.After(now) {
		if state.nextStart.Before(t.job.expiresAt) {
			t.deferred = state.nextStart
		}
		return false
	}
	if !exists && !p.reserveStateLocked(now) {
		return false
	}
	state.nextStart = now.Add(preloadAccountGap)
	p.accounts[key] = state
	t.job.state.attempts++
	t.signing = true
	return true
}

// Publish 只执行缓存 / 槽位内存写入；回调不得做日志、SQL、文件或网络 I/O。
func (t *PreloadTask) Publish(publish func()) bool {
	t.gate.Lock()
	defer t.gate.Unlock()
	t.owner.mu.Lock()
	allowed := t.signing
	t.owner.mu.Unlock()
	if publish == nil || !allowed || !t.Valid() {
		return false
	}
	publish()
	t.owner.mu.Lock()
	t.published = true
	t.owner.mu.Unlock()
	return true
}

// PreloadWindow 返回动态提前窗口，短片最多提前自身一半。
func PreloadWindow(duration time.Duration) time.Duration {
	if duration <= 0 {
		return 0
	}
	window := max(3*time.Minute, min(10*time.Minute, duration/10))
	return min(duration/2, window)
}

// ShouldPreloadNext 只计算合法媒体时间；媒体类型和可信会话由适配器检查。
func ShouldPreloadNext(duration, position time.Duration) bool {
	return duration > 0 && position >= 0 && position < duration && duration-position <= PreloadWindow(duration)
}

func (p *Preloader) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Preloader) reserveStateLocked(now time.Time) bool {
	if len(p.states)+len(p.accounts)+len(p.takeovers) >= preloadStateLimit {
		p.sweepLocked(now, true)
	}
	return len(p.states)+len(p.accounts)+len(p.takeovers) < preloadStateLimit
}

func (p *Preloader) sweepLocked(now time.Time, force bool) {
	if !force && now.Before(p.nextSweep) {
		return
	}
	p.nextSweep = now.Add(time.Minute)
	for key, state := range p.states {
		if state.job == nil && !state.expiresAt.After(now) {
			delete(p.states, key)
		}
	}
	for key, state := range p.accounts {
		if !state.nextStart.After(now) && !state.cooldown.After(now) {
			delete(p.accounts, key)
		}
	}
	for source, until := range p.takeovers {
		if !until.After(now) {
			delete(p.takeovers, source)
		}
	}
}

func preloadAccountKey(account PreloadAccount) PreloadAccount {
	// 同一网盘用户的本地账号别名共享间隔和冷却，不能通过换本地 ID 绕过。
	if account.UserID != "" {
		account.AccountID = 0
	}
	return account
}

func preloadJobAccount(job *preloadJob) PreloadAccount {
	if job.state.account.Provider != "" {
		return job.state.account
	}
	return job.candidate.Account
}

func (p *Preloader) accountAvailableLocked(job *preloadJob) bool {
	account := preloadJobAccount(job)
	for _, active := range p.active {
		if active == job {
			continue
		}
		other := preloadJobAccount(active)
		if account.Provider == "" || account.UserID == "" || other.Provider == "" || other.UserID == "" ||
			(account.AccountID != 0 && account.AccountID == other.AccountID) ||
			(account.Provider == other.Provider && account.UserID == other.UserID) {
			return false
		}
	}
	return true
}

func (p *Preloader) nextLocked(now time.Time) (*preloadJob, time.Time) {
	var selected *preloadJob
	var next time.Time
	for i := 0; i < len(p.queue); {
		job := p.queue[i]
		if !job.expiresAt.After(now) || !p.settings.Load().enabled || job.generation != p.settings.Load().generation {
			job.state.job = nil
			if job.state.metadata == 0 && job.state.attempts == 0 {
				delete(p.states, job.candidate.Key)
			}
			p.queue = append(p.queue[:i], p.queue[i+1:]...)
			continue
		}
		if state := p.accounts[preloadAccountKey(preloadJobAccount(job))]; state.nextStart.After(job.readyAt) {
			job.readyAt = state.nextStart
		}
		if next.IsZero() || job.expiresAt.Before(next) {
			next = job.expiresAt
		}
		if len(p.active) >= preloadWorkers || !p.accountAvailableLocked(job) {
			i++
			continue
		}
		if !job.readyAt.After(now) {
			if selected == nil || job.candidate.Kind < selected.candidate.Kind || (job.candidate.Kind == selected.candidate.Kind && job.sequence < selected.sequence) {
				selected = job
			}
		} else {
			ready := minTime(job.readyAt, job.expiresAt)
			if next.IsZero() || ready.Before(next) {
				next = ready
			}
		}
		i++
	}
	if selected != nil {
		for i, job := range p.queue {
			if job == selected {
				p.queue = append(p.queue[:i], p.queue[i+1:]...)
				break
			}
		}
		ctx, stop := context.WithTimeout(p.root, preloadRunLimit)
		selected.task = &PreloadTask{owner: p, job: selected, ctx: ctx, stop: stop, log: selected.candidate.Log}
		p.active = append(p.active, selected)
	}
	return selected, next
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (p *Preloader) work() {
	defer close(p.done)
	defer func() {
		p.mu.Lock()
		p.stopped = true
		p.settings.Store(&preloadSettings{generation: p.settings.Load().generation})
		tasks := p.cancelLocked(func(*preloadJob) bool { return true }, false)
		p.states = make(map[string]*preloadState)
		p.accounts = make(map[PreloadAccount]preloadAccountState)
		p.takeovers = make(map[string]time.Time)
		p.mu.Unlock()
		finishPreloadCancellation(tasks)
	}()
	var workers sync.WaitGroup
	workers.Go(p.workLoop)
	p.workLoop()
	workers.Wait()
}

func (p *Preloader) workLoop() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		if p.root.Err() != nil {
			return
		}
		p.mu.Lock()
		canceled := p.canceled
		p.canceled = 0
		p.mu.Unlock()
		if canceled > 0 {
			helpers.AppLogger.Debugf("已撤销预加载机会：原因=候选或播放关联已变化，数量=%d", canceled)
		}
		p.mu.Lock()
		job, readyAt := p.nextLocked(time.Now())
		p.mu.Unlock()
		if job != nil {
			p.signal()
			task := job.task
			started := time.Now()
			result, internalFailure := runPreload(job)
			event := p.finish(job, result, started)
			task.stop()
			p.signal()
			if internalFailure {
				p.Apply(false, p.settings.Load().generation+1)
				p.stop()
				helpers.AppLogger.Errorf("预加载暂时不可用，已停止后台任务，播放仍按正常流程处理")
				return
			}
			event.log()
			continue
		}
		var deadline <-chan time.Time
		if !readyAt.IsZero() {
			timer.Reset(max(0, time.Until(readyAt)))
			deadline = timer.C
		}
		select {
		case <-p.root.Done():
			return
		case <-p.wake:
			timer.Stop()
		case <-deadline:
		}
	}
}

func runPreload(job *preloadJob) (result PreloadResult, internalFailure bool) {
	defer func() {
		if recover() != nil {
			result.Outcome, internalFailure = PreloadFailed, true
		}
	}()
	return job.run(job.task.ctx, job.task), false
}

type preloadEvent struct {
	sequence uint64
	kind     PreloadKind
	account  uint
	attempts uint8
	result   PreloadResult
	elapsed  time.Duration
	level    helpers.LogLevel
	reason   string
	context  PreloadLogContext
	provider string
	cooldown time.Duration
}

func (p *Preloader) finish(job *preloadJob, result PreloadResult, started time.Time) preloadEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	now, task, state := time.Now(), job.task, job.state
	event := preloadEvent{sequence: job.sequence, kind: job.candidate.Kind, account: state.account.AccountID, attempts: state.attempts, result: result, elapsed: now.Sub(started), level: helpers.LogLevelDebug, reason: result.Reason, context: task.log, provider: preloadJobAccount(job).Provider}
	for i, active := range p.active {
		if active == job {
			p.active = append(p.active[:i], p.active[i+1:]...)
			break
		}
	}
	if p.states[job.candidate.Key] != state || state.job != job {
		event.reason = "candidate_changed"
		return event
	}
	if task.Valid() && len(p.queue) < preloadQueueLimit && job.expiresAt.After(now) &&
		(task.waitingAccount || (!task.deferred.IsZero() && task.deferred.Before(job.expiresAt))) {
		job.task = nil
		if !task.deferred.IsZero() {
			job.readyAt = task.deferred
		}
		p.queue = append(p.queue, job)
		event.reason = "account_interval"
		if task.waitingAccount {
			event.reason = "account_busy"
		}
		return event
	}
	state.job = nil
	state.terminal = true
	state.enrichable = result.Outcome == PreloadSkipped && !task.signing
	if result.Outcome == PreloadRisk || result.Outcome == PreloadUnauthorized {
		event.level, event.reason = helpers.LogLevelWarn, "risk"
		if result.Outcome == PreloadUnauthorized {
			event.reason = "unauthorized"
		}
		if state.account.AccountID != 0 {
			key := preloadAccountKey(state.account)
			account, exists := p.accounts[key]
			if account.cooldown.After(now) {
				event.level = helpers.LogLevelDebug
			} else if !exists {
				p.reserveStateLocked(now)
			}
			if exists || len(p.states)+len(p.accounts)+len(p.takeovers) < preloadStateLimit {
				account.cooldown = maxTime(account.cooldown, now.Add(max(preloadRiskWait, result.RetryAfter)))
				p.accounts[key] = account
				event.cooldown = account.cooldown.Sub(now)
			}
		}
		state.run, state.candidate.Run = nil, nil
		return event
	}
	if !task.Valid() {
		event.reason = task.cancelReason
		if event.reason == "" {
			event.reason = "canceled"
			if errors.Is(task.ctx.Err(), context.DeadlineExceeded) {
				event.reason = "timeout"
			}
		}
		state.run, state.candidate.Run = nil, nil
		if task.invalid.Load() && state.attempts == 0 && state.metadata == 0 {
			delete(p.states, job.candidate.Key)
		}
		return event
	}
	switch {
	case task.published:
		event.level, event.reason = helpers.LogLevelInfo, "published"
	case result.Outcome == PreloadCached:
		event.reason = "cached"
	case result.Outcome == PreloadTransient && !state.revoked && state.attempts < 2:
		state.terminal = false
		state.retryAt = now.Add(preloadRetryWait)
		event.reason = "transient_retry"
	case task.signing && (result.Outcome == PreloadFailed || result.Outcome == PreloadTransient):
		event.level = helpers.LogLevelWarn
		if event.reason == "" {
			event.reason = "provider_failed"
		}
	}
	if state.terminal {
		state.run, state.candidate.Run = nil, nil
	}
	return event
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
