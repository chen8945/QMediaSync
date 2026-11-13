package controllers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/helpers"
	embyclientrestgo "qmediasync/internal/medialibrary/client"
	"qmediasync/internal/models"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"
)

const embyTempImagePrefix = "qms_emby_"

type mediaNotifier struct {
	config *models.EmbyConfig
	client *embyclientrestgo.Client
	userID string
}

type newSeries struct {
	ID          string
	Name        string
	Seasons     map[int][]int
	Episodes    map[string]time.Time
	LastUpdated time.Time
	notifier    *mediaNotifier
}

type mediaNotificationRun struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	workers sync.WaitGroup
}

var seriesNotifications = struct {
	sync.Mutex
	added   map[string]newSeries
	deleted map[string]newSeries
	run     *mediaNotificationRun
}{added: make(map[string]newSeries), deleted: make(map[string]newSeries)}

// StartMediaLibraryNotifications 启动两端本地通知聚合，不等待远端服务器。
func StartMediaLibraryNotifications() error {
	seriesNotifications.Lock()
	defer seriesNotifications.Unlock()
	if previous := seriesNotifications.run; previous != nil {
		if previous.ctx.Err() == nil {
			return nil
		}
		select {
		case <-previous.done:
		default:
			return errors.New("媒体通知正在停止，请稍后重试")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &mediaNotificationRun{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	seriesNotifications.run = run
	seriesNotifications.added = make(map[string]newSeries)
	seriesNotifications.deleted = make(map[string]newSeries)
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		run.workers.Go(func() { startSeriesNotificationTicker(ctx, kind) })
	}
	go func() { <-ctx.Done(); run.workers.Wait(); close(run.done) }()
	return nil
}

// StopMediaLibraryNotifications 停止入队，取消并等待事件处理和两端通知发送。
func StopMediaLibraryNotifications(ctx context.Context) error {
	seriesNotifications.Lock()
	run := seriesNotifications.run
	if run != nil {
		run.cancel()
	}
	seriesNotifications.Unlock()
	if run == nil {
		return nil
	}
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func queueMediaWebhook(work func(context.Context)) {
	seriesNotifications.Lock()
	defer seriesNotifications.Unlock()
	run := seriesNotifications.run
	if run == nil || run.ctx.Err() != nil {
		return
	}
	run.workers.Go(func() { work(run.ctx) })
}

var playbackNotifications = struct {
	sync.Mutex
	events map[string]time.Time
}{events: make(map[string]time.Time)}

func newMediaNotifier(config *models.EmbyConfig) *mediaNotifier {
	return &mediaNotifier{config: config, client: config.Client()}
}

func (n *mediaNotifier) productName() string {
	if n.config.MediaServerType == models.MediaServerJellyfin {
		return "Jellyfin"
	}
	return "Emby"
}

func (n *mediaNotifier) scopeKey() string {
	return string(n.config.MediaServerType) + ":" + models.EmbyServerConfigIdentity(n.config)
}

func (n *mediaNotifier) enabled() bool {
	current, err := models.ReadMediaLibraryConfigSnapshot(n.config.MediaServerType)
	return err == nil && current.Enabled && current.EnableMediaNotification == 1 &&
		models.EmbyServerConfigIdentity(current) == models.EmbyServerConfigIdentity(n.config)
}

func (n *mediaNotifier) detail(ctx context.Context, itemID string) *embyclientrestgo.BaseItemDtoV2 {
	if n.userID == "" {
		users, err := n.client.GetUsersWithAllLibrariesAccessContext(ctx)
		if err != nil || len(users) == 0 {
			return nil
		}
		n.userID = users[0].ID
	}
	item, err := n.client.GetItemDetailByUserContext(ctx, itemID, n.userID)
	if err != nil {
		return nil
	}
	return item
}

func (n *mediaNotifier) addEpisode(seriesID string, season, episode int, itemID, seriesName string, fallback time.Time, deleted bool) {
	if !n.enabled() {
		return
	}
	key := n.scopeKey() + ":" + seriesID
	seriesNotifications.Lock()
	defer seriesNotifications.Unlock()
	if run := seriesNotifications.run; run == nil || run.ctx.Err() != nil {
		return
	}
	buffer := seriesNotifications.added
	if deleted {
		buffer = seriesNotifications.deleted
	}
	series, exists := buffer[key]
	if !exists {
		series = newSeries{ID: seriesID, Name: seriesName, Seasons: make(map[int][]int), Episodes: make(map[string]time.Time), notifier: n}
	}
	series.Seasons[season] = append(series.Seasons[season], episode)
	if fallback.After(series.Episodes[itemID]) {
		series.Episodes[itemID] = fallback
	}
	series.LastUpdated = time.Now()
	buffer[key] = series
}

// takeSeriesNotifications 只转移本端已到期数据，网络和通知发送在锁外执行。
func takeSeriesNotifications(now time.Time, kind models.MediaServerType) (added, deleted []newSeries) {
	seriesNotifications.Lock()
	defer seriesNotifications.Unlock()
	for key, series := range seriesNotifications.added {
		if series.notifier.config.MediaServerType == kind && now.Sub(series.LastUpdated) >= 10*time.Second {
			added = append(added, series)
			delete(seriesNotifications.added, key)
		}
	}
	for key, series := range seriesNotifications.deleted {
		if series.notifier.config.MediaServerType == kind && now.Sub(series.LastUpdated) >= 10*time.Second {
			deleted = append(deleted, series)
			delete(seriesNotifications.deleted, key)
		}
	}
	return
}

func startSeriesNotificationTicker(ctx context.Context, kind models.MediaServerType) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			added, deleted := takeSeriesNotifications(now, kind)
			for _, series := range added {
				sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				series.notifier.sendNewSeries(sendCtx, series)
				cancel()
			}
			for _, series := range deleted {
				sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				series.notifier.sendDeletedSeries(sendCtx, series)
				cancel()
			}
		}
	}
}

var notificationTemplate = `
{{title}} ({{year}})

🆔 评分：{{rate}}
🎬 类型：{{genes}}
👤 主演：{{actors}}
⏰ 入库时间：{{addedTime}}

📝 简介
{{overview}}
`

func mediaAddedContent(detail *embyclientrestgo.BaseItemDtoV2, added time.Time) string {
	rate := "暂无数据"
	if detail.CommunityRating > 0 {
		rate = fmt.Sprintf("%.1f", detail.CommunityRating)
	}
	genres := strings.Join(detail.Genres, ", ")
	if genres == "" {
		genres = "暂无数据"
	}
	actors := make([]string, 0, 5)
	for _, person := range detail.People {
		if person.Type == "Actor" {
			actors = append(actors, person.Name)
		}
		if len(actors) == 5 {
			break
		}
	}
	actorNames := strings.Join(actors, ", ")
	if actorNames == "" {
		actorNames = "暂无数据"
	}
	overview := detail.Overview
	if overview == "" {
		overview = "暂无简介"
	}
	return strings.NewReplacer("{{title}}", detail.Name, "{{year}}", fmt.Sprint(detail.ProductionYear), "{{rate}}", rate,
		"{{genes}}", genres, "{{actors}}", actorNames, "{{addedTime}}", added.Format("2006-01-02 15:04:05"), "{{overview}}", overview).Replace(notificationTemplate)
}

func (n *mediaNotifier) sendNewMovie(ctx context.Context, itemID string, fallback time.Time) {
	if !n.enabled() {
		return
	}
	detail := n.detail(ctx, itemID)
	if detail == nil {
		return
	}
	content := mediaAddedContent(detail, parseEmbyNotificationTime(detail.DateCreated, fallback))
	n.send(ctx, &notification.Notification{Type: models.MediaAdded, Title: "📚 " + n.productName() + " 电影入库通知", Content: content}, detail)
}

func (n *mediaNotifier) sendNewSeries(ctx context.Context, series newSeries) {
	if !n.enabled() {
		return
	}
	detail := n.detail(ctx, series.ID)
	if detail == nil {
		return
	}
	content := mediaAddedContent(detail, newSeriesAddedTime(ctx, n.client, series.Episodes))
	seasonEpisodes := formatSeasonEpisodes(series.Seasons)
	if seasonEpisodes != "" {
		content = strings.Replace(content, "⏰ 入库时间：", "📺 入库季集："+seasonEpisodes+"\n\n⏰ 入库时间：", 1)
	}
	n.send(ctx, &notification.Notification{Type: models.MediaAdded, Title: "📚 " + n.productName() + " 电视剧入库通知", Content: content}, detail)
}

// notificationEventWithHistory 只采用本次收件冻结的本端名称，不借展示字段补造删除授权。
func notificationEventWithHistory(event EmbyEvent, record models.EmbyWebhookRecord) EmbyEvent {
	if event.Event != "library.deleted" || record.ServerID != event.Server.ID {
		return event
	}
	var input models.EmbyDeletionInput
	if json.Unmarshal([]byte(record.InputJSON), &input) != nil || input.ServerID != record.ServerID {
		return event
	}
	for _, owner := range input.Owners {
		item := owner.Item
		if item.ItemId != event.Item.ID || item.ServerId != record.ServerID {
			continue
		}
		if item.Name != "" {
			event.Item.Name = item.Name
		}
		if item.SeriesName != "" {
			event.Item.SeriesName = item.SeriesName
		}
		event.Item.SeriesId, event.Item.SeasonId = item.SeriesId, item.SeasonId
		event.Item.ParentIndexNumber, event.Item.IndexNumber = item.ParentIndexNumber, item.IndexNumber
		break
	}
	return event
}

func (n *mediaNotifier) sendDeletedMovie(ctx context.Context, itemName string) {
	n.send(ctx, &notification.Notification{Type: models.MediaRemoved, Title: "🗑️ " + n.productName() + " 媒体删除通知",
		Content: fmt.Sprintf("电影名称：%s\n⏰ 删除时间：%s", itemName, time.Now().Format("2006-01-02 15:04:05"))}, nil)
}

func (n *mediaNotifier) sendDeletedSeries(ctx context.Context, series newSeries) {
	n.send(ctx, &notification.Notification{Type: models.MediaRemoved, Title: "🗑️ " + n.productName() + " 媒体删除通知",
		Content: fmt.Sprintf("电视剧名称：%s\n删除季集：%s\n⏰ 删除时间：%s", series.Name, formatSeasonEpisodes(series.Seasons), time.Now().Format("2006-01-02 15:04:05"))}, nil)
}

func (n *mediaNotifier) send(ctx context.Context, notif *notification.Notification, detail *embyclientrestgo.BaseItemDtoV2) {
	if !n.enabled() || ctx.Err() != nil {
		return
	}
	if detail != nil {
		notif.Image = n.image(ctx, detail)
	}
	if notif.Image != "" {
		defer func() {
			if err := removeEmbyTempImage(notif.Image); err != nil && !os.IsNotExist(err) {
				helpers.AppLogger.Warnf("删除通知临时图片失败：%v", err)
			}
		}()
	}
	if !n.enabled() || ctx.Err() != nil {
		return
	}
	notif.Content = "媒体服务器：" + n.productName() + "\n" + notif.Content
	notif.Timestamp, notif.Priority = time.Now(), notification.NormalPriority
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif); err != nil {
			helpers.AppLogger.Errorf("发送媒体通知失败：%v", err)
		}
	}
}

func (n *mediaNotifier) image(ctx context.Context, detail *embyclientrestgo.BaseItemDtoV2) string {
	imageType := ""
	if len(detail.BackdropImageTags) > 0 || detail.ImageTags["backdrop"] != "" || detail.ImageTags["Backdrop"] != "" {
		imageType = "Backdrop"
	} else if detail.ImageTags["Primary"] != "" {
		imageType = "Primary"
	}
	if imageType == "" {
		return ""
	}
	body, _, err := n.client.GetItemImageContext(ctx, detail.Id, imageType)
	if err != nil {
		helpers.AppLogger.Warnf("读取媒体通知图片失败：%v", err)
		return ""
	}
	path, err := createEmbyTempImagePath(detail.Id)
	if err != nil {
		return ""
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		_ = removeEmbyTempImage(path)
		return ""
	}
	return path
}

func acceptPlaybackNotification(key string, now time.Time) bool {
	playbackNotifications.Lock()
	defer playbackNotifications.Unlock()
	if previous, exists := playbackNotifications.events[key]; exists && now.Sub(previous) < time.Minute {
		return false
	}
	playbackNotifications.events[key] = now
	for key, timestamp := range playbackNotifications.events {
		if now.Sub(timestamp) > 5*time.Minute {
			delete(playbackNotifications.events, key)
		}
	}
	return true
}

func (n *mediaNotifier) handlePlayback(ctx context.Context, webhook models.EmbyPlaybackWebhook) {
	if !n.enabled() || webhook.GetUserID() == "" {
		return
	}
	key := n.scopeKey() + ":" + fmt.Sprintf("%s_%s_%s_%s_%s", webhook.GetUserID(), webhook.Item.Type, webhook.Item.Name, webhook.GetDeviceName(), webhook.Event)
	if !acceptPlaybackNotification(key, time.Now()) {
		return
	}
	detail := n.detail(ctx, webhook.Item.ID)
	if detail != nil {
		webhook.Item.Name, webhook.Item.SeriesName = detail.Name, detail.SeriesName
		webhook.Item.SeasonNumber, webhook.Item.EpisodeNumber = detail.ParentIndexNumber, detail.IndexNumber
	}
	notif := &notification.Notification{Type: notification.NotificationType(webhook.GetNotificationEventType()),
		Title:   fmt.Sprintf("%s %s %s %s", webhook.GetEventTypeEmoji(), n.productName(), webhook.GetEventTypeName(), webhook.Item.Name),
		Content: formatPlaybackNotificationContent(n.config, &webhook, detail), Metadata: map[string]any{}}
	if duration := webhook.GetPlaybackDuration(); duration > 0 {
		notif.Metadata["观看时长"] = models.FormatPlaybackDuration(duration)
	}
	if detail == nil {
		detail = &embyclientrestgo.BaseItemDtoV2{Id: webhook.Item.ID, ImageTags: webhook.Item.ImageTags}
	}
	n.send(ctx, notif, detail)
}

func formatPlaybackNotificationContent(config *models.EmbyConfig, webhook *models.EmbyPlaybackWebhook, detail *embyclientrestgo.BaseItemDtoV2) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "用户：%s\n设备：%s (%s)\n", webhook.GetUserName(), webhook.GetDeviceName(), webhook.GetClientName())
	if webhook.Item.Type == "Episode" {
		fmt.Fprintf(&buf, "电视剧：%s\n季集：S%dE%d\n", webhook.Item.SeriesName, webhook.Item.SeasonNumber, webhook.Item.EpisodeNumber)
	}
	if config.EnablePlaybackProgress == 1 {
		position, runtime := webhook.PlaybackInfo.PositionTicks, webhook.PlaybackInfo.MediaSource.RunTimeTicks
		if position > 0 && runtime > 0 {
			fmt.Fprintf(&buf, "播放进度：%s / %s (%.0f%%)\n", formatTicksToTime(position), formatTicksToTime(runtime), float64(position)/float64(runtime)*100)
		} else if runtime > 0 {
			fmt.Fprintf(&buf, "时长：%s\n", formatTicksToTime(runtime))
		}
	}
	if config.EnablePlaybackOverview == 1 && detail != nil && detail.Overview != "" {
		overview := []rune(detail.Overview)
		if len(overview) > 100 {
			overview = append(overview[:100], '…')
		}
		fmt.Fprintf(&buf, "简介：%s\n", string(overview))
	}
	return buf.String()
}

func createEmbyTempImagePath(itemID string) (string, error) {
	sum := sha256.Sum256([]byte(itemID))
	pattern := fmt.Sprintf("%s%x_*.jpg", embyTempImagePrefix, sum)
	file, err := os.CreateTemp(os.TempDir(), pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func removeEmbyTempImage(imagePath string) error {
	if strings.TrimSpace(imagePath) == "" {
		return nil
	}
	tempDir, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(imagePath)
	if err != nil {
		return err
	}
	if filepath.Dir(absPath) != tempDir {
		return fmt.Errorf("拒绝删除临时目录外的媒体图片: %s", imagePath)
	}
	if !isEmbyTempImageName(filepath.Base(absPath)) {
		return fmt.Errorf("拒绝删除非受控媒体临时图片: %s", imagePath)
	}
	return os.Remove(absPath)
}

func isEmbyTempImageName(name string) bool {
	if !strings.HasPrefix(name, embyTempImagePrefix) || !strings.HasSuffix(name, ".jpg") {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, embyTempImagePrefix), ".jpg")
	if len(body) <= 65 || body[64] != '_' {
		return false
	}
	for _, r := range body[:64] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return len(body[65:]) > 0
}

func parseEmbyNotificationTime(value string, fallback time.Time) time.Time {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil && !parsed.IsZero() {
		return parsed.In(time.Local)
	}
	return fallback.In(time.Local)
}

func newSeriesAddedTime(ctx context.Context, client *embyclientrestgo.Client, episodes map[string]time.Time) time.Time {
	ids := make([]string, 0, len(episodes))
	for id := range episodes {
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	created := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += 100 {
		query := embyclientrestgo.EmbyItemsQuery{
			IDs:              strings.Join(ids[start:min(start+100, len(ids))], ","),
			Fields:           "DateCreated",
			IncludeItemTypes: "Episode",
		}
		err := client.FetchMediaItemsByLibraryID(ctx, query, func(item embyclientrestgo.BaseItemDtoV2) error {
			if item.Type == "Episode" {
				created[item.Id] = item.DateCreated
			}
			return nil
		})
		if err != nil {
			helpers.AppLogger.Warnf("查询入库单集时间失败，缺失的时间使用事件或接收时间")
		}
	}
	var latest time.Time
	for id, fallback := range episodes {
		if added := parseEmbyNotificationTime(created[id], fallback); added.After(latest) {
			latest = added
		}
	}
	return latest
}

func formatSeasonEpisodes(seasons map[int][]int) string {
	if len(seasons) == 0 {
		return ""
	}

	seasonNumbers := make([]int, 0, len(seasons))
	for seasonNumber := range seasons {
		seasonNumbers = append(seasonNumbers, seasonNumber)
	}
	sort.Ints(seasonNumbers)

	seasonStrArr := make([]string, 0, len(seasons))
	for _, seasonNumber := range seasonNumbers {
		episodes := seasons[seasonNumber]
		if len(episodes) == 0 {
			continue
		}
		// 去重处理，避免同一集多次触发事件导致重复显示
		episodes = removeDuplicates(episodes)

		sort.Ints(episodes)
		seasonStr := fmt.Sprintf("S%d", seasonNumber)

		start := episodes[0]
		prev := episodes[0]
		for i := 1; i < len(episodes); i++ {
			if episodes[i] != prev+1 {
				if start == prev {
					seasonStr += fmt.Sprintf("E%d, ", start)
				} else {
					seasonStr += fmt.Sprintf("E%d-E%d, ", start, prev)
				}
				start = episodes[i]
			}
			prev = episodes[i]
		}
		if start == prev {
			seasonStr += fmt.Sprintf("E%d, ", start)
		} else {
			seasonStr += fmt.Sprintf("E%d-E%d, ", start, prev)
		}

		seasonStr = strings.TrimSuffix(seasonStr, ", ")
		seasonStrArr = append(seasonStrArr, seasonStr)
	}

	return strings.Join(seasonStrArr, "; ")
}

func removeDuplicates(episodes []int) []int {
	seen := make(map[int]struct{})
	result := make([]int, 0, len(episodes))
	for _, ep := range episodes {
		if _, exists := seen[ep]; !exists {
			seen[ep] = struct{}{}
			result = append(result, ep)
		}
	}
	return result
}

func formatTicksToTime(ticks int64) string {
	totalSeconds := ticks / 10000000 // ticks to seconds
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
