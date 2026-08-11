<template>
  <div class="main-content-container emby-content">
    <PageHeader />

    <div class="emby-settings-wrapper">
      <el-form
        :model="embyData"
        :rules="formRules"
        :label-position="isMobile ? 'top' : 'left'"
        :label-width="200"
        class="emby-form"
        ref="formRef"
      >
        <el-card class="settings-card emby-server-card" shadow="hover">
          <template #header>
            <div class="card-header-wrapper">
              <div class="card-header-icon server-icon">
                <el-icon :size="24"><Monitor /></el-icon>
              </div>
              <div class="card-header-content">
                <h2 class="card-title">Emby 服务器配置</h2>
                <p class="card-subtitle">配置 Emby 服务器连接信息</p>
              </div>
            </div>
          </template>

          <el-form-item label="Emby 服务器地址" prop="emby_url">
            <el-input
              v-model="embyData.emby_url"
              placeholder="请输入 Emby 服务器地址，格式：http://ip:port"
              :disabled="embyLoading"
              class="limited-width-input"
              @input="updateEmbyExample"
              :prefix-icon="Link"
              clearable
            />
            <p style="color: red; font-weight: bold; margin-left: 16px; font-size: 28px">
              填写 Emby 地址后，请点击页面底部的“保存设置”，并重启 QMediaSync 使配置生效
            </p>
            <div v-if="embyExample" class="emby-example-inline">
              <span class="example-label">示例格式：</span>
              <code class="example-url">{{ embyExample }}</code>
            </div>
            <div class="form-help">
              <el-icon><InfoFilled /></el-icon>
              <span
                >如需使用 Emby 外网 302，请填写 Emby 服务器地址，不要以 / 结尾。一般填写 Emby
                内网地址，例如：http://192.168.1.100:8096，也可以填写 Docker 容器地址</span
              >
            </div>
          </el-form-item>

          <el-form-item label="Emby API Key" prop="emby_api_key">
            <el-input
              v-model="embyData.emby_api_key"
              placeholder="请输入 Emby API Key"
              :disabled="embyLoading"
              class="limited-width-input"
              @input="updateEmbyExample"
              :prefix-icon="Key"
            />
            <div class="form-help">
              <el-icon><InfoFilled /></el-icon>
              <span
                >API Key 用于提取 STRM 文件的视频、音频和内封字幕信息；不需要该功能时可以不填</span
              >
            </div>
            <div class="form-help author-credit">
              <span
                >STRM 信息提取功能由<a
                  href="https://github.com/truewhile"
                  target="_blank"
                  rel="noopener noreferrer"
                  >@truewhile</a
                >
                提供，感谢分享。</span
              >
            </div>
          </el-form-item>
        </el-card>

        <el-card class="settings-card webhook-card" shadow="hover">
          <template #header>
            <div class="card-header-wrapper">
              <div class="card-header-icon webhook-icon">
                <el-icon :size="24"><Connection /></el-icon>
              </div>
              <div class="card-header-content">
                <h3 class="card-title">通知链接配置</h3>
                <p class="card-subtitle">配置 Emby 与 QMediaSync 的通知连接</p>
              </div>
            </div>
          </template>

          <el-form-item label="Emby 通知链接">
            <el-input
              v-model="webhookUrl"
              readonly
              class="limited-width-input webhook-input"
              :prefix-icon="Link"
            >
              <template #append>
                <el-button @click="copyWebhookUrl" :icon="DocumentCopy">复制</el-button>
              </template>
            </el-input>
            <div class="form-help">
              <el-icon><InfoFilled /></el-icon>
              <span>将此链接配置到 Emby 的通知设置中，</span>
              <a
                href="https://github.com/qicfan/qmediasync/wiki/Emby-%E9%80%9A%E7%9F%A5%E9%85%8D%E7%BD%AE"
                target="_blank"
                rel="noopener noreferrer"
                class="help-link"
                >配置教程</a
              >
              <a
                :href="embyData.emby_url + '/web/index.html#!/settings/notifications.html'"
                target="_blank"
                rel="noopener noreferrer"
                class="help-link action-link"
                >去配置</a
              >
            </div>
            <div class="form-help" v-if="embyData.enable_auth">
              <el-icon><WarningFilled /></el-icon>
              <span class="warning-text"
                >已开启鉴权，请在 Emby 的通知链接中追加 API Key 参数，示例：<code
                  class="inline-code"
                  >{{ webhookUrl }}?api_key=你的 API Key</code
                >。也支持通过请求头
                <code class="inline-code">X-API-Key: 你的 API Key</code> 传递</span
              >
            </div>
          </el-form-item>

          <el-form-item label="通知链接鉴权" prop="enable_auth">
            <div class="switch-wrapper">
              <el-switch
                v-model="embyData.enable_auth"
                :active-value="1"
                :inactive-value="0"
                :disabled="embyLoading"
                active-color="#67c23a"
                inactive-color="#dcdfe6"
              />
              <span class="switch-label" :class="{ 'is-active': embyData.enable_auth }">
                {{ embyData.enable_auth ? '已启用鉴权' : '已禁用鉴权' }}
              </span>
            </div>
            <div class="form-help">
              <el-icon><InfoFilled /></el-icon>
              <span
                >启用后，Emby Webhook 请求必须通过
                <code class="inline-code">X-API-Key</code> 请求头或
                <code class="inline-code">api_key</code> 查询参数携带 API Key
                才会生效。外网使用通知链接时建议开启。请到<router-link
                  to="/settings/api-keys"
                  class="help-link"
                  >API Key 模块</router-link
                >生成</span
              >
            </div>
          </el-form-item>

          <el-form-item label="播放通知显示剧情简介" prop="enable_playback_overview">
            <div class="switch-wrapper">
              <el-switch
                v-model="embyData.enable_playback_overview"
                :active-value="1"
                :inactive-value="0"
                :disabled="embyLoading"
                active-color="#67c23a"
                inactive-color="#dcdfe6"
              />
              <span
                class="switch-label"
                :class="{ 'is-active': embyData.enable_playback_overview }"
              >
                {{ embyData.enable_playback_overview ? '已启用' : '已禁用' }}
              </span>
            </div>
            <div class="form-help">
              <el-icon><InfoFilled /></el-icon>
              <span>开启后，播放通知将显示当前视频的剧情简介（超过 100 字自动截断）</span>
            </div>
          </el-form-item>

          <el-form-item label="播放通知显示播放进度" prop="enable_playback_progress">
            <div class="switch-wrapper">
              <el-switch
                v-model="embyData.enable_playback_progress"
                :active-value="1"
                :inactive-value="0"
                :disabled="embyLoading"
                active-color="#67c23a"
                inactive-color="#dcdfe6"
              />
              <span
                class="switch-label"
                :class="{ 'is-active': embyData.enable_playback_progress }"
              >
                {{ embyData.enable_playback_progress ? '已启用' : '已禁用' }}
              </span>
            </div>
            <div class="form-help">
              <el-icon><InfoFilled /></el-icon>
              <span>开启后，播放通知将显示当前播放进度和总时长（如：00:15:30 / 00:45:00）</span>
            </div>
          </el-form-item>
        </el-card>

        <el-card class="settings-card sync-features-card" shadow="hover">
          <template #header>
            <div class="card-header-wrapper">
              <div class="card-header-icon features-icon">
                <el-icon :size="24"><Setting /></el-icon>
              </div>
              <div class="card-header-content">
                <h3 class="card-title">同步和功能配置</h3>
                <p class="card-subtitle">配置 STRM 同步与媒体库联动功能</p>
              </div>
            </div>
          </template>

          <div class="feature-item">
            <el-form-item label="入库后提取媒体信息" prop="enable_extract_media_info">
              <div class="switch-wrapper">
                <el-switch
                  v-model="embyData.enable_extract_media_info"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="embyLoading"
                  active-color="#67c23a"
                  inactive-color="#dcdfe6"
                />
                <span
                  class="switch-label"
                  :class="{ 'is-active': embyData.enable_extract_media_info }"
                >
                  {{ embyData.enable_extract_media_info ? '启用' : '禁用' }}
                </span>
              </div>
            </el-form-item>
            <div class="feature-description">
              <div class="config-links">
                <span>需要在 Emby 中配置通知后才会生效，</span>
                <a
                  href="https://github.com/qicfan/qmediasync/wiki/Emby-%E9%80%9A%E7%9F%A5%E9%85%8D%E7%BD%AE"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="help-link"
                  >配置教程</a
                >
                <a
                  :href="embyData.emby_url + '/web/index.html#!/settings/notifications.html'"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="help-link action-link"
                  >去配置</a
                >
              </div>
              <p class="feature-note">
                QMediaSync 收到 Emby
                入库通知后，会自动提取该资源的视频、音频、内封字幕等媒体信息，帮助 Emby 更快起播。
              </p>
            </div>
          </div>

          <el-divider class="feature-divider" />

          <div class="feature-item">
            <el-form-item label="启用同步" prop="sync_enabled">
              <div class="switch-wrapper">
                <el-switch
                  v-model="embyData.sync_enabled"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="embyLoading"
                  active-color="#67c23a"
                  inactive-color="#dcdfe6"
                />
                <span class="switch-label" :class="{ 'is-active': embyData.sync_enabled }">
                  {{ embyData.sync_enabled ? '已启用' : '已禁用' }}
                </span>
              </div>
            </el-form-item>
            <div class="feature-description">
              <p class="feature-note">
                启用后会把 Emby 条目同步到 QMediaSync
                本地数据库，并与网盘文件建立关联，用于刷新媒体库和联动删除网盘文件。
              </p>
            </div>
          </div>

          <!-- Emby 条目同步范围选择卡片 - 只有启用同步时才显示 -->
          <el-card
            v-if="embyData.sync_enabled === 1"
            class="settings-card library-selection-card"
            shadow="hover"
          >
            <template #header>
              <div class="card-header-wrapper">
                <div class="card-header-icon library-icon">
                  <el-icon :size="24"><FolderOpened /></el-icon>
                </div>
                <div class="card-header-content">
                  <h3 class="card-title">媒体库条目同步选择</h3>
                  <p class="card-subtitle">选择需要同步条目的 Emby 媒体库</p>
                </div>
              </div>
            </template>

            <el-form-item label="同步模式">
              <el-radio-group v-model="embyData.sync_all_libraries" @change="handleSyncModeChange">
                <el-radio :value="1">全部媒体库</el-radio>
                <el-radio :value="0">指定媒体库</el-radio>
              </el-radio-group>
              <div class="form-help">
                <el-icon><InfoFilled /></el-icon>
                <span
                  >选择“全部媒体库”会同步所有媒体库中的条目（包括未来新增的媒体库）；选择“指定媒体库”可手动选择需要同步条目的媒体库</span
                >
              </div>
            </el-form-item>

            <el-form-item label="选择媒体库" v-if="embyData.sync_all_libraries === 0">
              <el-checkbox-group v-model="selectedLibraryIds" class="library-checkbox-group">
                <el-checkbox
                  v-for="lib in availableLibraries"
                  :key="lib.library_id"
                  :label="lib.library_id"
                  class="library-checkbox"
                >
                  {{ lib.name }}
                </el-checkbox>
              </el-checkbox-group>
              <div class="form-help" v-if="availableLibraries.length === 0">
                <el-icon><WarningFilled /></el-icon>
                <span class="warning-text"
                  >请先配置 Emby 服务器地址并保存，然后执行一次同步以获取媒体库列表</span
                >
              </div>
            </el-form-item>
          </el-card>

          <el-divider class="feature-divider" />

          <div class="feature-item" :class="{ 'is-disabled': !embyData.sync_enabled }">
            <el-form-item label="同步时间" prop="sync_cron">
              <el-input
                v-model="embyData.sync_cron"
                :placeholder="`请输入 Cron 表达式，如：${CRON_DEFAULTS.embySync}`"
                :disabled="embyLoading || !embyData.sync_enabled"
                class="limited-width-input"
                @blur="fetchCronNextTimes"
                clearable
              />
              <div class="form-help">
                <el-icon><InfoFilled /></el-icon>
                <span
                  >Cron 表达式，格式：分 时 日 月 周（如：{{
                    CRON_DEFAULTS.embySync
                  }}
                  表示每小时整点执行）</span
                >
              </div>
            </el-form-item>
            <div v-if="cronNextTimes.length > 0" class="cron-next-times">
              <div class="cron-times-header">
                <el-icon><Clock /></el-icon>
                <span>接下来 5 次执行时间：</span>
              </div>
              <ul class="cron-times-list">
                <li v-for="(time, index) in cronNextTimes" :key="index">{{ time }}</li>
              </ul>
            </div>
            <el-form-item label="每日首次全量同步" prop="enable_daily_first_full_sync">
              <div class="switch-wrapper">
                <el-switch
                  v-model="embyData.enable_daily_first_full_sync"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="embyLoading || !embyData.sync_enabled"
                  active-color="#67c23a"
                  inactive-color="#dcdfe6"
                />
                <span
                  class="switch-label"
                  :class="{ 'is-active': embyData.enable_daily_first_full_sync }"
                >
                  {{ embyData.enable_daily_first_full_sync ? '启用' : '禁用' }}
                </span>
              </div>
              <div class="form-help">
                <el-icon><InfoFilled /></el-icon>
                <span>启用后，每天第一次定时同步执行全量同步，之后当天定时同步执行增量同步</span>
              </div>
            </el-form-item>
          </div>

          <el-divider class="feature-divider" />

          <div class="feature-item" :class="{ 'is-disabled': !embyData.sync_enabled }">
            <el-form-item label="同步后刷新媒体库" prop="enable_refresh_library">
              <div class="switch-wrapper">
                <el-switch
                  v-model="embyData.enable_refresh_library"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="embyLoading || !embyData.sync_enabled"
                  active-color="#67c23a"
                  inactive-color="#dcdfe6"
                />
                <span
                  class="switch-label"
                  :class="{ 'is-active': embyData.enable_refresh_library }"
                >
                  {{ embyData.enable_refresh_library ? '启用' : '禁用' }}
                </span>
              </div>
            </el-form-item>
            <div class="feature-description">
              <p>
                该功能需要先完成一次 Emby 条目同步。如果下方同步管理卡片中的总项目数为
                0，请点击“启动同步”先同步一次条目。
              </p>
              <p>
                STRM 同步完成后不会立即刷新。系统会按 Emby
                媒体库合并刷新请求，并等待相关同步任务和下载任务结束；下载状态变化会每 5
                秒合并处理一次，避免大量小文件频繁触发检查。
              </p>
              <p>
                如果等待超过 6
                小时仍未满足刷新条件，本次刷新任务会自动取消，不会强制刷新仍有下载任务的媒体库。建议同时开启
                Emby 实时监控作为补充。
              </p>
              <p class="feature-note">
                某个 STRM 同步目录完成后，会提交关联 Emby
                媒体库的刷新任务；系统确认相关下载和同步队列稳定后再刷新媒体库，让新增 STRM
                文件入库。
              </p>
            </div>
          </div>

          <el-divider class="feature-divider" />

          <div class="feature-item danger-item" :class="{ 'is-disabled': !embyData.sync_enabled }">
            <el-form-item label="删除时联动删除网盘文件" prop="enable_delete_netdisk">
              <div class="switch-wrapper">
                <el-switch
                  v-model="embyData.enable_delete_netdisk"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="embyLoading || !embyData.sync_enabled"
                  active-color="#f56c6c"
                  inactive-color="#dcdfe6"
                />
                <span class="switch-label" :class="{ 'is-danger': embyData.enable_delete_netdisk }">
                  {{ embyData.enable_delete_netdisk ? '启用' : '禁用' }}
                </span>
              </div>
            </el-form-item>
            <div class="feature-description">
              <el-alert type="warning" :closable="false" class="danger-alert">
                <template #default>
                  <strong>⚠ 谨慎启用：</strong>
                  启用后，在 Emby 中删除项目时，对应的网盘文件也会被删除<br />
                  <strong
                    >由于 Emby 的处理机制，STRM
                    文件内容变更时可能会先触发删除再新增，因此可能出现：STRM 变更→Emby
                    通知删除→QMediaSync 联动删除网盘→Emby 新增项目→播放失败</strong
                  >，目前还没有可靠的规避方式<br />
                  <strong
                    >如果开启了 Emby 实时监控，在文件系统中删除 STRM
                    文件或文件夹也会触发删除通知，并可能联动删除网盘文件。所有删除操作都要谨慎。</strong
                  >
                </template>
              </el-alert>
              <div class="config-links">
                <span>需要在 Emby 中配置通知后才会生效，</span>
                <a
                  href="https://github.com/qicfan/qmediasync/wiki/Emby-%E9%80%9A%E7%9F%A5%E9%85%8D%E7%BD%AE"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="help-link"
                  >配置教程</a
                >
                <a
                  :href="embyData.emby_url + '/web/index.html#!/settings/notifications.html'"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="help-link action-link"
                  >去配置</a
                >
              </div>
              <ul class="delete-rules">
                <li>在 Emby 中删除电影时，会在网盘中一并删除视频文件的父目录</li>
                <li>在 Emby 中删除剧集条目时，会删除网盘中 tvshow.nfo 所在的父目录</li>
                <li>
                  在 Emby
                  中删除季时，会先检查视频文件的父目录；如果父目录是季文件夹，则删除该文件夹；如果父目录是包含
                  tvshow.nfo 的目录，则只删除该季下所有集对应的视频文件和元数据（NFO、封面）
                </li>
                <li>在 Emby 中删除单集时，会删除对应的视频文件和元数据（NFO、封面）</li>
              </ul>
            </div>
          </div>
        </el-card>

        <ResponsiveActionBar :is-mobile="isMobile">
          <template #actions>
            <el-button
              type="success"
              @click="saveEmbyConfig"
              :loading="embyLoading"
              :icon="Check"
              size="large"
              class="save-btn"
            >
              保存设置
            </el-button>
            <el-button
              type="primary"
              @click="praseEmby"
              :loading="embyLoading"
              :icon="Refresh"
              :disabled="!embyData.emby_url || !embyData.emby_api_key"
              size="large"
              class="extract-btn"
            >
              提取媒体信息
            </el-button>
          </template>
          <template #help>
            <p>
              该功能会为 Emby 中尚未提取媒体信息的项目触发提取。如果刚重建媒体库或刚接入新的
              Emby，可以手动执行一次。进度和详情可在<router-link
                to="/download-queue"
                class="help-link"
                >下载队列页</router-link
              >查看
            </p>
          </template>
        </ResponsiveActionBar>
      </el-form>

      <el-alert
        v-if="embyStatus"
        :title="embyStatus.title"
        :type="embyStatus.type"
        :description="embyStatus.description"
        :closable="false"
        show-icon
        class="emby-status-alert"
      />

      <el-card class="sync-management-card" shadow="hover">
        <template #header>
          <div class="card-header-wrapper">
            <div class="card-header-icon sync-icon">
              <el-icon :size="24"><Refresh /></el-icon>
            </div>
            <div class="card-header-content">
              <h3 class="card-title">同步管理</h3>
              <p class="card-subtitle">管理 Emby 条目同步到本地状态</p>
            </div>
            <div class="card-header-action">
              <el-button
                type="primary"
                @click="startSync"
                :loading="syncStartLoading"
                :icon="Refresh"
                :disabled="isStartSyncDisabled"
                size="default"
              >
                {{ isSyncRunning ? '同步进行中…' : '启动全量同步' }}
              </el-button>
            </div>
          </div>
        </template>

        <div v-if="syncInfo" class="sync-info-grid">
          <div class="sync-stat-card">
            <div class="stat-icon status-icon">
              <el-icon :size="28"><Refresh /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">当前状态</div>
              <div class="stat-value" :class="{ 'is-enabled': isSyncRunning }">
                {{ currentSyncModeLabel }}
              </div>
              <div v-if="syncInfo.started_at" class="stat-helper">
                开始：{{ formatSyncAbsoluteTime(syncInfo.started_at) }}
              </div>
            </div>
          </div>

          <div class="sync-stat-card">
            <div class="stat-icon auto-sync-icon">
              <el-icon :size="28"><Timer /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">自动同步</div>
              <div class="stat-value" :class="{ 'is-enabled': syncInfo.sync_enabled }">
                {{ syncInfo.sync_enabled ? '已启用' : '已禁用' }}
              </div>
            </div>
          </div>

          <div class="sync-stat-card">
            <div class="stat-icon cycle-icon">
              <el-icon :size="28"><Clock /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">同步周期</div>
              <div class="stat-value">{{ syncInfo.sync_cron }}</div>
            </div>
          </div>

          <div class="sync-stat-card">
            <div class="stat-icon items-icon">
              <el-icon :size="28"><FolderOpened /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">已同步 Item 数</div>
              <div class="stat-value highlight">{{ syncInfo.total_items || 0 }}</div>
            </div>
          </div>

          <div class="sync-stat-card">
            <div class="stat-icon time-icon">
              <el-icon :size="28"><Calendar /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">最近成功同步</div>
              <div class="stat-value">{{ formatSyncRelativeTime(syncInfo.last_sync_time) }}</div>
              <div class="stat-helper">{{ lastSuccessSyncHelper }}</div>
            </div>
          </div>

          <div class="sync-stat-card">
            <div class="stat-icon time-icon">
              <el-icon :size="28"><Calendar /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">最近增量同步</div>
              <div class="stat-value">
                {{ formatSyncRelativeTime(syncInfo.last_incremental_sync_at) }}
              </div>
              <div class="stat-helper">
                {{ formatSyncAbsoluteTime(syncInfo.last_incremental_sync_at) }}
              </div>
            </div>
          </div>

          <div class="sync-stat-card">
            <div class="stat-icon time-icon">
              <el-icon :size="28"><Calendar /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">最近全量同步</div>
              <div class="stat-value">{{ formatSyncRelativeTime(syncInfo.last_full_sync_at) }}</div>
              <div class="stat-helper">
                {{ formatSyncAbsoluteTime(syncInfo.last_full_sync_at) }}
              </div>
            </div>
          </div>

          <div class="sync-stat-card">
            <div class="stat-icon items-icon">
              <el-icon :size="28"><FolderOpened /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">最近处理数量</div>
              <div class="stat-value highlight">{{ syncInfo.last_processed_count || 0 }}</div>
            </div>
          </div>

          <div v-if="syncInfo.last_error" class="sync-stat-card error-card">
            <div class="stat-icon error-icon">
              <el-icon :size="28"><WarningFilled /></el-icon>
            </div>
            <div class="stat-content">
              <div class="stat-label">最近错误</div>
              <div class="stat-value error-text">{{ syncInfo.last_error }}</div>
            </div>
          </div>
        </div>

        <div v-if="isSyncRunning" class="sync-progress">
          <div class="progress-indicator">
            <el-icon class="sync-progress-loading-icon" :size="20"><Loading /></el-icon>
            <span>{{ currentSyncModeLabel }}进行中，请稍候…</span>
          </div>
        </div>

        <div v-if="!syncInfo" class="sync-empty">
          <el-empty description="暂无同步数据，请点击上方按钮启动同步" :image-size="80" />
        </div>
      </el-card>

      <el-alert title="使用提示" type="info" :closable="false" show-icon class="tips-alert">
        <template #default>
          填写 Emby 服务器地址和 API Key 后，即可触发媒体信息提取。提取完成后，Emby
          会显示视频、音频和内封字幕信息，并支持切换字幕。<br />
          如果需要提取，可以点击上方的“提取媒体信息”按钮。
        </template>
      </el-alert>
    </div>
  </div>
</template>

<script setup lang="ts">
import { SERVER_URL } from '@/const'
import { CRON_DEFAULTS, HTTP_URL_PATTERN } from '@/constants/validation'
import { useHttpClient } from '@/http/client'
import {
  Check,
  Refresh,
  Loading,
  Monitor,
  Link,
  Key,
  Connection,
  Setting,
  DocumentCopy,
  InfoFilled,
  WarningFilled,
  Timer,
  Clock,
  FolderOpened,
  Calendar,
} from '@element-plus/icons-vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { computed, onMounted, ref, reactive, onBeforeUnmount, useTemplateRef } from 'vue'
import PageHeader from '@/components/common/PageHeader.vue'
import ResponsiveActionBar from '@/components/common/ResponsiveActionBar.vue'
import { useDeviceType } from '@/composables/useDeviceType'
import { copyText } from '@/utils/clipboard'
import { formatMaybeUnixDateTime, formatRelativeTime } from '@/utils/timeUtils'

const http = useHttpClient()

const formRef = useTemplateRef<FormInstance>('formRef')

const { isMobile } = useDeviceType()

const embyLoading = ref(false)

const syncStartLoading = ref(false)
const syncPolling = ref(false)
type EmbySyncMode = 'idle' | 'full' | 'incremental' | 'webhook' | 'refresh_library' | ''

interface EmbySyncInfo {
  sync_enabled: boolean
  sync_cron: string
  total_items: number
  last_sync_time: number | null
  last_full_sync_at?: number | null
  last_incremental_sync_at?: number | null
  last_saved_cursor_at?: number | null
  last_processed_count?: number | null
  last_success_sync_mode?: EmbySyncMode
  last_error?: string
  is_running?: boolean
  sync_mode?: EmbySyncMode
  started_at?: number | null
}

const syncInfo = ref<EmbySyncInfo | null>(null)
let syncPollTimer: number | null = null

const syncModeLabels: Record<Exclude<EmbySyncMode, ''>, string> = {
  idle: '空闲',
  full: '全量同步 Emby 条目',
  incremental: '增量同步 Emby 条目',
  webhook: 'Webhook 单条同步 Emby 条目',
  refresh_library: '刷新 Emby 媒体库',
}

const isSyncRunning = computed(() => Boolean(syncInfo.value?.is_running || syncPolling.value))

const currentSyncModeLabel = computed(() => {
  const mode = syncInfo.value?.sync_mode || 'idle'
  if (mode in syncModeLabels) {
    return syncModeLabels[mode as Exclude<EmbySyncMode, ''>]
  }
  return isSyncRunning.value ? '同步 Emby 条目' : syncModeLabels.idle
})

const lastSuccessSyncModeLabel = computed(() => {
  const mode = syncInfo.value?.last_success_sync_mode
  if (!mode || mode === 'idle' || !(mode in syncModeLabels)) {
    return ''
  }
  return syncModeLabels[mode as Exclude<EmbySyncMode, ''>]
})

const lastSuccessSyncHelper = computed(() => {
  if (!syncInfo.value?.last_sync_time) {
    return ''
  }
  return [formatSyncAbsoluteTime(syncInfo.value.last_sync_time), lastSuccessSyncModeLabel.value]
    .filter(Boolean)
    .join(' · ')
})

const isStartSyncDisabled = computed(
  () => !embyData.emby_url || !embyData.sync_enabled || isSyncRunning.value,
)

const cronNextTimes = ref<string[]>([])

const embyData = reactive({
  emby_url: '',
  emby_api_key: '',
  sync_enabled: 1,
  sync_cron: CRON_DEFAULTS.embySync,
  enable_refresh_library: 1,
  enable_extract_media_info: 1,
  enable_delete_netdisk: 0,
  enable_auth: 1,
  sync_all_libraries: 1,
  selected_libraries: '[]',
  enable_daily_first_full_sync: 1,
  enable_playback_overview: 0,
  enable_playback_progress: 0,
})

interface EmbyLibraryOption {
  library_id: string
  name: string
}

// 媒体库选择相关数据
const availableLibraries = ref<EmbyLibraryOption[]>([])
const selectedLibraryIds = ref<string[]>([])

const embyExample = ref('http://192.168.1.100:8096')

const webhookUrl = ref('')
const updateWebhookUrl = () => {
  let baseUrl: string
  if (SERVER_URL === '/api') {
    baseUrl = window.location.origin
  } else {
    baseUrl = SERVER_URL.replace(/\/api$/, '')
  }
  webhookUrl.value = `${baseUrl}/emby/webhook`
}

const embyStatus = ref<{
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
} | null>(null)

const formRules: FormRules = {
  emby_url: [
    {
      message: '请输入 Emby 服务器地址',
      trigger: 'blur',
    },
    {
      pattern: HTTP_URL_PATTERN,
      message: '请输入有效的 URL 格式，如：http://ip:port',
      trigger: 'blur',
    },
  ],
}

const defaultConfig = {
  emby_url: '',
  emby_api_key: '',
  sync_enabled: 1,
  sync_cron: CRON_DEFAULTS.embySync,
  enable_refresh_library: 1,
  enable_extract_media_info: 1,
  enable_delete_netdisk: 0,
  enable_auth: 1,
  sync_all_libraries: 1,
  selected_libraries: '[]',
  enable_daily_first_full_sync: 1,
}

const loadEmbyConfig = async () => {
  try {
    embyLoading.value = true
    const response = await http.get(`${SERVER_URL}/setting/emby-config`)

    if (response?.data.code === 200) {
      if (response.data.data?.exists && response.data.data?.config) {
        const config = response.data.data.config
        embyData.emby_url = config.emby_url || ''
        embyData.emby_api_key = config.emby_api_key || ''
        embyData.sync_enabled = config.sync_enabled ?? 1
        embyData.sync_cron = config.sync_cron || CRON_DEFAULTS.embySync
        embyData.enable_refresh_library = config.enable_refresh_library ?? 1
        embyData.enable_extract_media_info = config.enable_extract_media_info ?? 1
        embyData.enable_delete_netdisk = config.enable_delete_netdisk ?? 0
        embyData.enable_auth = config.enable_auth ?? 1
        embyData.sync_all_libraries = config.sync_all_libraries ?? 1
        embyData.selected_libraries = config.selected_libraries || '[]'
        embyData.enable_daily_first_full_sync = config.enable_daily_first_full_sync ?? 1
        embyData.enable_playback_overview = config.enable_playback_overview ?? 0
        embyData.enable_playback_progress = config.enable_playback_progress ?? 0

        // 解析选中的媒体库 ID 列表
        try {
          selectedLibraryIds.value = JSON.parse(embyData.selected_libraries)
        } catch {
          selectedLibraryIds.value = []
        }

        // 加载媒体库列表
        await loadEmbyLibraries()
      } else {
        Object.assign(embyData, defaultConfig)
      }
    } else {
      Object.assign(embyData, defaultConfig)
      ElMessage.warning('加载 Emby 配置失败，使用默认配置')
    }
  } catch (error) {
    console.error('加载 Emby 配置错误：', error)
    Object.assign(embyData, defaultConfig)
    ElMessage.error('加载 Emby 配置失败')
  } finally {
    embyLoading.value = false
  }
}

// 加载 Emby 媒体库列表
const loadEmbyLibraries = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/emby/libraries`)
    if (response?.data.code === 200 && response?.data.data) {
      availableLibraries.value = (response.data.data as EmbyLibraryOption[]).map((lib) => ({
        library_id: lib.library_id,
        name: lib.name,
      }))
    }
  } catch (error) {
    console.error('加载媒体库列表错误：', error)
  }
}

// 处理同步模式切换
const handleSyncModeChange = (value: number) => {
  // 当选择"指定媒体库"时，重新加载媒体库列表
  if (value === 0) {
    loadEmbyLibraries()
  }
}

const saveEmbyConfig = async () => {
  if (!formRef.value) return

  try {
    await formRef.value.validate()
    embyLoading.value = true

    const response = await http.post(
      `${SERVER_URL}/setting/emby-config`,
      {
        emby_url: embyData.emby_url.trim(),
        emby_api_key: embyData.emby_api_key.trim(),
        sync_enabled: embyData.sync_enabled,
        sync_cron: embyData.sync_cron,
        enable_refresh_library: embyData.enable_refresh_library,
        enable_extract_media_info: embyData.enable_extract_media_info,
        enable_delete_netdisk: embyData.enable_delete_netdisk,
        enable_auth: embyData.enable_auth,
        sync_all_libraries: embyData.sync_all_libraries,
        selected_libraries: JSON.stringify(selectedLibraryIds.value),
        enable_daily_first_full_sync: embyData.enable_daily_first_full_sync,
        enable_playback_overview: embyData.enable_playback_overview,
        enable_playback_progress: embyData.enable_playback_progress,
      },
      {
        headers: {
          'Content-Type': 'application/json',
        },
      },
    )

    if (response?.data.code === 200) {
      embyStatus.value = {
        title: '保存成功',
        type: 'success',
        description: 'Emby 配置已成功保存',
      }
      await loadEmbyConfig()
    } else {
      embyStatus.value = {
        title: '保存失败',
        type: 'error',
        description: response?.data.message || '保存 Emby 配置失败',
      }
      ElMessage.error(response?.data.message || '保存失败')
    }

    setTimeout(() => {
      embyStatus.value = null
    }, 3000)
  } catch (error) {
    console.error('保存 Emby 配置错误：', error)
    embyStatus.value = {
      title: '保存失败',
      type: 'error',
      description: '保存 Emby 配置时出现错误',
    }
    ElMessage.error('保存失败')
  } finally {
    embyLoading.value = false
  }
}

const praseEmby = async () => {
  try {
    embyLoading.value = true
    const response = await http.post(
      `${SERVER_URL}/setting/emby/parse`,
      {
        emby_url: embyData.emby_url.trim(),
        emby_api_key: embyData.emby_api_key.trim(),
      },
      {
        headers: {
          'Content-Type': 'application/json',
        },
      },
    )

    if (response?.data.code === 200) {
      embyStatus.value = {
        title: '触发提取媒体信息成功',
        type: 'success',
        description: '已成功触发提取媒体信息',
      }
      ElMessage.success('已成功触发提取媒体信息')
    } else {
      embyStatus.value = {
        title: '触发提取媒体信息失败',
        type: 'error',
        description: response?.data.message || '触发提取媒体信息失败',
      }
      ElMessage.error(response?.data.message || '触发提取媒体信息失败')
    }
  } catch (error) {
    console.error('触发提取媒体信息错误：', error)
    embyStatus.value = {
      title: '触发提取媒体信息失败',
      type: 'error',
      description: '触发提取媒体信息时出现错误',
    }
    ElMessage.error('触发提取媒体信息失败')
  } finally {
    embyLoading.value = false
  }
}

const updateEmbyExample = () => {}

const copyWebhookUrl = async () => {
  if (await copyText(webhookUrl.value)) {
    ElMessage.success('Webhook 链接已复制到剪贴板')
    return
  }
  ElMessage.error('复制失败，请手动复制')
}

const fetchCronNextTimes = async () => {
  if (!embyData.sync_cron || !embyData.sync_cron.trim()) {
    cronNextTimes.value = []
    return
  }

  try {
    const response = await http.get(`${SERVER_URL}/setting/cron`, {
      params: { cron: embyData.sync_cron.trim() },
    })

    if (response?.data.code === 200 && response.data.data) {
      cronNextTimes.value = response.data.data.map((value: string | number) =>
        formatMaybeUnixDateTime(value),
      )
    } else {
      cronNextTimes.value = []
      if (response?.data.message) {
        ElMessage.warning(response.data.message)
      }
    }
  } catch (error) {
    console.error('获取 Cron 执行时间错误：', error)
    cronNextTimes.value = []
    ElMessage.error('获取 Cron 执行时间失败，请检查表达式格式')
  }
}

const startSync = async () => {
  try {
    syncStartLoading.value = true
    const response = await http.post(`${SERVER_URL}/emby/sync/start`)

    if (response?.data.code === 200) {
      ElMessage.success('同步已启动')
      syncPolling.value = true
      await querySyncStatus()
    } else {
      ElMessage.error(response?.data.message || '启动同步失败')
    }
  } catch (error) {
    console.error('启动同步错误：', error)
    ElMessage.error('启动同步失败')
  } finally {
    syncStartLoading.value = false
  }
}

const querySyncStatus = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/emby/sync/status`)

    if (response?.data.code === 200) {
      syncInfo.value = response.data.data
      syncPolling.value = response.data.data?.is_running
      if (syncPolling.value) {
        startSyncPolling()
      } else {
        stopSyncPolling()
      }
    }
  } catch (error) {
    console.error('查询同步状态错误：', error)
  }
}

const startSyncPolling = () => {
  if (syncPollTimer !== null) {
    return
  }
  syncPollTimer = window.setInterval(async () => {
    try {
      const response = await http.get(`${SERVER_URL}/emby/sync/status`)

      if (response?.data.code === 200) {
        syncInfo.value = response.data.data
        syncPolling.value = response.data.data?.is_running
        if (!syncPolling.value) {
          stopSyncPolling()
        }
      }
    } catch (error) {
      console.error('轮询同步状态错误：', error)
    }
  }, 3000)
}

const stopSyncPolling = () => {
  syncPolling.value = false
  if (syncPollTimer !== null) {
    clearInterval(syncPollTimer)
    syncPollTimer = null
  }
}

const formatSyncRelativeTime = (timestamp: number | null | undefined) => {
  const formatted = formatRelativeTime(timestamp)
  return formatted === '-' ? '未同步' : formatted
}

const formatSyncAbsoluteTime = (timestamp: number | null | undefined) => {
  const formatted = formatMaybeUnixDateTime(timestamp)
  return formatted === '-' ? '' : formatted
}

onMounted(() => {
  loadEmbyConfig()
  querySyncStatus()
  updateWebhookUrl()
})

onBeforeUnmount(() => {
  stopSyncPolling()
})
</script>

<style scoped lang="css">
.emby-content {
  display: block;
  background: transparent;
}

.emby-settings-wrapper {
  /* max-width: 1400px; */
  margin: 0 auto;
}

.emby-form {
  display: flex;
  flex-direction: column;
  gap: 20px;
  margin-bottom: 24px;
}

.settings-card,
.sync-management-card {
  border-radius: 12px;
  border: 1px solid #dcdfe6;
  background: #fff;
  overflow: hidden;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.06);
  transition:
    transform 0.2s ease,
    box-shadow 0.2s ease;
  width: 100%;
}

.settings-card:hover,
.sync-management-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
}

.settings-card :deep(.el-card__header),
.sync-management-card :deep(.el-card__header) {
  padding: 18px 20px;
  border-bottom: 1px solid #dcdfe6;
}

.settings-card :deep(.el-card__body),
.sync-management-card :deep(.el-card__body) {
  background: #fff;
}

.card-header-wrapper {
  display: flex;
  align-items: center;
  gap: 16px;
}

.card-header-icon {
  width: 48px;
  height: 48px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
}

.server-icon {
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
}

.webhook-icon {
  background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
}

.features-icon {
  background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%);
}

.sync-icon {
  background: linear-gradient(135deg, #43e97b 0%, #38f9d7 100%);
}

.library-icon {
  background: linear-gradient(135deg, #8b5cf6 0%, #06b6d4 100%);
}

.card-header-content {
  flex: 1;
}

.card-title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

.card-subtitle {
  margin: 4px 0 0;
  font-size: 13px;
  color: #909399;
}

.card-header-action {
  margin-left: auto;
}

.limited-width-input {
  max-width: 600px;
}

.webhook-input :deep(.el-input__wrapper) {
  background-color: #f5f7fa;
}

.emby-example-inline {
  margin-top: 10px;
  padding: 12px 16px;
  background: linear-gradient(135deg, #f5f7fa 0%, #e8eef5 100%);
  border-radius: 8px;
  display: flex;
  align-items: center;
  gap: 12px;
  border: 1px solid #e4e7ed;
}

.example-label {
  font-weight: 500;
  color: #606266;
  font-size: 13px;
}

.example-url {
  color: #409eff;
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
  font-size: 13px;
  background: white;
  padding: 4px 8px;
  border-radius: 4px;
  border: 1px solid #d9ecff;
}

.form-help {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-size: 14px;
  color: #3c3d40;
  margin-top: 8px;
  line-height: 1.5;
}

.form-help .el-icon {
  margin-top: 2px;
  flex-shrink: 0;
}

.author-credit {
  margin-top: 4px;
}

.help-link {
  color: #409eff;
  text-decoration: none;
  font-weight: 500;
  transition: color 0.2s;
}

.help-link:hover {
  color: #66b1ff;
  text-decoration: underline;
}

.action-link {
  margin-left: 8px;
  padding: 2px 8px;
  background: #ecf5ff;
  border-radius: 4px;
  font-size: 12px;
}

.action-link:hover {
  background: #d9ecff;
}

.inline-code {
  background: #fef0f0;
  padding: 2px 6px;
  border-radius: 3px;
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
  font-size: 11px;
  color: #f56c6c;
}

.switch-wrapper {
  display: flex;
  align-items: center;
  gap: 12px;
}

.switch-label {
  font-size: 14px;
  color: #909399;
  font-weight: 500;
  transition: color 0.3s;
}

.switch-label.is-active {
  color: #67c23a;
}

.switch-label.is-danger {
  color: #f56c6c;
}

.warning-text {
  color: #e6a23c;
}

.feature-item {
  padding: 16px 0;
}

.feature-item:first-child {
  padding-top: 0;
}

.feature-item.is-disabled {
  opacity: 0.5;
  pointer-events: none;
}

.danger-item {
  background: linear-gradient(135deg, #fef6f6 0%, #fff 100%);
  margin: 0 -20px;
  padding: 20px;
  border-radius: 8px;
  border: 1px solid #fde2e2;
}

.danger-alert {
  margin-bottom: 12px;
  border-radius: 8px;
}

.feature-description {
  font-size: 13px;
  color: #606266;
  line-height: 1.8;
  margin-top: 8px;
}

.feature-description p {
  margin: 6px 0;
}

.feature-note {
  color: #909399;
  font-style: italic;
}

.cron-next-times {
  margin-top: 12px;
  padding: 12px 16px;
  background: linear-gradient(135deg, #f0f9ff 0%, #e0f2fe 100%);
  border-radius: 8px;
  border: 1px solid #bae6fd;
}

.cron-times-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 500;
  color: #0284c7;
  margin-bottom: 8px;
}

.cron-times-list {
  margin: 0;
  padding-left: 20px;
  list-style: decimal;
}

.cron-times-list li {
  font-size: 13px;
  color: #0369a1;
  line-height: 1.8;
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
}

.feature-divider {
  margin: 16px 0;
}

.config-links {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 8px;
}

.delete-rules {
  margin: 12px 0 0;
  padding-left: 20px;
  color: #909399;
}

.delete-rules li {
  margin: 6px 0;
  line-height: 1.6;
}

.save-btn,
.extract-btn {
  min-width: 140px;
}

.sync-management-card {
  margin-bottom: 20px;
  width: 100%;
}

.sync-info-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 20px;
}

.sync-stat-card {
  display: flex;
  align-items: center;
  width: 100%;
  gap: 16px;
  padding: 20px;
  background: linear-gradient(135deg, #f8f9fa 0%, #ffffff 100%);
  border-radius: 12px;
  border: 1px solid #ebeef5;
  transition:
    transform 0.2s ease,
    box-shadow 0.2s ease;
}

.sync-stat-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
}

.stat-icon {
  width: 56px;
  height: 56px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
}

.auto-sync-icon {
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
}

.cycle-icon {
  background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
}

.items-icon {
  background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%);
}

.time-icon {
  background: linear-gradient(135deg, #fa709a 0%, #fee140 100%);
}

.status-icon {
  background: linear-gradient(135deg, #22c55e 0%, #14b8a6 100%);
}

.error-icon {
  background: linear-gradient(135deg, #ef4444 0%, #f97316 100%);
}

.stat-content {
  flex: 1;
  min-width: 0;
}

.stat-label {
  font-size: 13px;
  color: #909399;
  margin-bottom: 4px;
}

.stat-value {
  font-size: 18px;
  font-weight: 600;
  color: #303133;
  overflow-wrap: anywhere;
}

.stat-value.is-enabled {
  color: #67c23a;
}

.stat-value.highlight {
  color: #409eff;
}

.stat-helper {
  margin-top: 4px;
  font-size: 12px;
  color: #909399;
  line-height: 1.4;
  overflow-wrap: anywhere;
}

.error-card {
  border-color: #fde2e2;
  background: linear-gradient(135deg, #fff5f5 0%, #ffffff 100%);
}

.error-text {
  color: #f56c6c;
  font-size: 14px;
}

.sync-progress {
  margin-top: 20px;
  padding: 16px;
  background: linear-gradient(135deg, #ecf5ff 0%, #f0f9ff 100%);
  border-radius: 8px;
  border: 1px solid #d9ecff;
}

.progress-indicator {
  display: flex;
  align-items: center;
  gap: 12px;
  color: #409eff;
  font-weight: 500;
}

.sync-progress-loading-icon {
  animation: rotating 2s linear infinite;
}

@keyframes rotating {
  0% {
    transform: rotate(0deg);
  }

  100% {
    transform: rotate(360deg);
  }
}

.sync-empty {
  padding: 20px;
}

.emby-status-alert {
  margin-bottom: 20px;
  border-radius: 8px;
}

.tips-alert {
  border-radius: 8px;
}

@media (max-width: 1200px) {
  .sync-info-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}

@media (max-width: 768px) {
  .emby-content {
    padding: 12px;
  }

  .emby-settings-wrapper {
    max-width: 100%;
  }

  .emby-form {
    gap: 16px;
  }

  .settings-card,
  .sync-management-card {
    border-radius: 8px;
  }

  .settings-card:hover,
  .sync-management-card:hover {
    transform: none;
  }

  .card-header-wrapper {
    flex-wrap: wrap;
  }

  .card-header-icon {
    width: 40px;
    height: 40px;
  }

  .card-header-icon .el-icon {
    font-size: 20px;
  }

  .card-title {
    font-size: 16px;
  }

  .card-subtitle {
    font-size: 12px;
  }

  .card-header-action {
    width: 100%;
    margin-top: 12px;
  }

  .card-header-action .el-button {
    width: 100%;
  }

  .limited-width-input {
    max-width: 100%;
  }

  .emby-example-inline {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .form-help {
    flex-wrap: wrap;
  }

  .config-links {
    flex-direction: column;
    align-items: flex-start;
  }

  .config-links .help-link {
    margin-top: 4px;
  }

  .action-link {
    margin-left: 0;
    margin-top: 4px;
  }

  .sync-management-card {
    border-radius: 8px;
  }

  .sync-info-grid {
    grid-template-columns: 1fr;
    gap: 12px;
  }

  .sync-stat-card {
    padding: 14px;
    gap: 12px;
  }

  .sync-stat-card:hover {
    transform: none;
  }

  .stat-icon {
    width: 44px;
    height: 44px;
  }

  .stat-icon .el-icon {
    font-size: 22px;
  }

  .stat-label {
    font-size: 12px;
  }

  .stat-value {
    font-size: 15px;
  }

  .sync-progress {
    padding: 12px;
  }

  .feature-item {
    padding: 12px 0;
  }

  .danger-item {
    margin: 0 -12px;
    padding: 12px;
    border-radius: 0;
  }

  .danger-alert {
    font-size: 12px;
  }

  .delete-rules {
    font-size: 12px;
  }

  .delete-rules li {
    margin: 4px 0;
  }

  .emby-status-alert,
  .tips-alert {
    border-radius: 8px;
  }
}

@media (max-width: 480px) {
  .emby-content {
    padding: 8px;
  }

  .card-header-icon {
    width: 36px;
    height: 36px;
  }

  .card-header-icon .el-icon {
    font-size: 18px;
  }

  .card-title {
    font-size: 15px;
  }

  .switch-wrapper {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .switch-label {
    font-size: 13px;
  }

  .feature-description {
    font-size: 12px;
  }

  .sync-stat-card {
    padding: 12px;
  }

  .stat-icon {
    width: 40px;
    height: 40px;
  }

  .stat-icon .el-icon {
    font-size: 20px;
  }

  .stat-value {
    font-size: 14px;
  }
}
</style>
