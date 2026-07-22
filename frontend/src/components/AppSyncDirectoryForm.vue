<template>
  <div class="sync-directory-form-page" :class="{ 'is-mobile': checkIsMobile }">
    <el-button type="primary" :icon="ArrowLeft" @click="goBack" size="large" link>
      返回 STRM 同步目录
    </el-button>

    <template v-if="checkIsMobile">
      <div class="mobile-form-header">
        <h3>{{ isEditMode ? '编辑同步目录' : '添加同步目录' }}</h3>
      </div>

      <el-form
        ref="formRef"
        :model="form"
        :rules="formRules"
        label-width="160px"
        label-position="top"
      >
        <el-form-item
          label="同步源类型"
          prop="source_type"
          :error="getSyncPathFieldError('source_type')"
          v-if="!isEditMode"
        >
          <el-select
            v-model="form.source_type"
            placeholder="请选择同步源类型"
            @change="handleSourceTypeChange"
          >
            <el-option
              v-for="typeItem in sourceTypeOptions"
              :key="typeItem.value"
              :label="typeItem.label"
              :value="typeItem.value"
            ></el-option>
          </el-select>
          <div class="form-tip">
            <div v-if="form.source_type === 'local'">
              如果通过 CD2 挂载其他网盘，请把 CD2 的本地挂载目录映射到容器中（Docker
              部署时尤其需要），然后选择该目录
            </div>
            <div v-if="form.source_type === '115'">需要先添加用于同步的 115 账号并授权</div>
            <div v-if="form.source_type === '123'">需要先添加用于同步的 123 账号并授权</div>
          </div>
        </el-form-item>
        <el-form-item
          label="网盘账号"
          prop="account_id"
          :error="getSyncPathFieldError('account_id')"
          v-if="form.source_type !== 'local' && !isEditMode"
        >
          <el-select
            v-model="form.account_id"
            placeholder="请选择网盘账号"
            :loading="accountsLoading"
            :disabled="loading"
          >
            <el-option
              v-for="account in accounts"
              :key="account.id"
              :label="account.name"
              :value="account.id"
            ></el-option>
          </el-select>
          <div class="form-tip">选择用于同步的网盘账号</div>
        </el-form-item>
        <el-form-item
          label="来源路径"
          prop="base_cid"
          :error="getSyncPathFieldError('base_cid')"
          v-if="
            (form.source_type !== 'local' && form.account_id) ||
            form.source_type === 'local' ||
            isEditMode
          "
        >
          <div class="pan-dir-input">
            <el-input
              v-model="form.base_cid"
              placeholder="点击选择按钮选择网盘目录"
              :disabled="loading"
              readonly
            />
            <el-button type="primary" @click="openDirSelector(false)" :disabled="loading">
              选择目录
            </el-button>
          </div>
          <div v-if="selectedDirPath" class="selected-path-inline">
            <span class="path-label">选中目录路径：</span>
            <code class="path-url">{{ selectedDirPath }}</code>
          </div>
          <div class="form-tip">选择网盘中要同步的目录</div>
        </el-form-item>
        <el-form-item
          label="目标路径"
          prop="local_path"
          :error="getSyncPathFieldError('local_path')"
          v-if="
            (form.source_type !== 'local' && form.account_id) ||
            form.source_type === 'local' ||
            isEditMode
          "
        >
          <div class="pan-dir-input">
            <el-input
              v-model="form.local_path"
              placeholder="点击选择按钮选择本地目录"
              :disabled="loading"
              readonly
            />
            <el-button type="primary" @click="openDirSelector(true)" :disabled="loading">
              选择目录
            </el-button>
          </div>
          <div class="form-tip">选择本地目录作为 STRM 文件的存放位置</div>
        </el-form-item>

        <el-form-item
          label="STRM 存放目录"
          v-if="
            (form.source_type !== 'local' && form.account_id) ||
            form.source_type === 'local' ||
            isEditMode
          "
        >
          <el-input
            v-model="form.strm_path"
            placeholder="自动计算：本地目录 + 选中目录路径"
            :disabled="true"
            readonly
          />
          <div class="form-tip">STRM 和元数据实际存放目录（自动生成）</div>
        </el-form-item>

        <el-form-item label="自定义设置" prop="custom_config">
          <el-switch
            v-model="form.custom_config"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">
            开启后可自定义视频扩展名和元数据扩展名配置，否则使用 STRM 设置中的值
          </div>
        </el-form-item>

        <template v-if="form.custom_config">
          <el-form-item label="启用定时同步" prop="enable_cron">
            <el-switch
              v-model="form.enable_cron"
              :active-value="true"
              :inactive-value="false"
              :disabled="loading"
            />
            <div class="form-tip">开启后将按照定时表达式自动执行同步任务</div>
          </el-form-item>
          <el-form-item label="定时同步表达式" prop="cron">
            <el-input
              v-model="form.cron"
              placeholder="留空则使用 STRM 设置中的表达式"
              :disabled="loading"
              @blur="loadCronTimes"
            />
            <div class="form-help">
              <p><strong>常用示例：</strong></p>
              <ul class="cron-examples">
                <li><code>0 0 * * *</code> - 每天 0 点执行</li>
                <li><code>0 */6 * * *</code> - 每 6 小时执行一次</li>
                <li><code>0 2 * * *</code> - 每天凌晨 2 点执行</li>
              </ul>
              <div v-if="cronTimes.length > 0" class="cron-next-times">
                <p><strong>下 5 次执行时间：</strong></p>
                <div v-loading="cronTimesLoading" class="cron-times-list">
                  <div v-for="(time, index) in cronTimes" :key="index" class="cron-time-item">
                    <el-tag type="info" size="small">{{ time }}</el-tag>
                  </div>
                </div>
              </div>
            </div>
          </el-form-item>
          <el-form-item label="STRM 直连地址" prop="strm_base_url">
            <el-input
              v-model="form.strm_base_url"
              placeholder="留空则使用 STRM 设置中的地址"
              :disabled="loading"
              @input="updateStrmExample"
            />
            <div v-if="strmExample" class="strm-example-inline">
              <span class="example-label">示例：</span>
              <code class="example-url">{{ strmExample }}</code>
            </div>
            <div class="form-tip">
              STRM 文件将使用此地址作为基础 URL，留空则使用 STRM 设置中的值
            </div>
          </el-form-item>
          <el-form-item label="最小视频文件大小 (MB)" prop="min_video_size">
            <el-slider
              v-model="form.min_video_size"
              :min="-1"
              :max="1000"
              :step="1"
              :precision="0"
              :format-tooltip="formatTooltip"
              show-input
            />
            <div class="form-help">
              <p>
                小于此大小的视频文件将不会生成 STRM 文件，单位为 MB。设置为 0 表示不限制文件大小
              </p>
            </div>
          </el-form-item>
          <el-form-item label="视频扩展名" prop="video_ext">
            <div class="ext-input-wrapper">
              <MetadataExtInput
                v-model="form.video_ext"
                placeholder="输入扩展名后按回车添加，也可用逗号或换行分隔"
                class="meta-ext-input limited-width-input"
              />
              <el-button
                type="primary"
                link
                @click="importFromStrmSettings('video_ext')"
                :loading="importStrmSettingsLoading"
              >
                从 STRM 设置导入
              </el-button>
            </div>
            <div class="form-tip">指定需要生成 STRM 文件的视频文件扩展名</div>
          </el-form-item>
          <el-form-item label="元数据扩展名" prop="meta_ext">
            <div class="ext-input-wrapper">
              <MetadataExtInput
                v-model="form.meta_ext"
                placeholder="输入扩展名后按回车添加，也可用逗号或换行分隔"
                class="meta-ext-input limited-width-input"
              />
              <el-button
                type="primary"
                link
                @click="importFromStrmSettings('meta_ext')"
                :loading="importStrmSettingsLoading"
              >
                从 STRM 设置导入
              </el-button>
            </div>
            <div class="form-tip">指定需要同步的元数据文件扩展名</div>
          </el-form-item>
          <el-form-item label="排除文件名" prop="exclude_name">
            <MetadataExtInput
              v-model="form.exclude_name"
              :autoAddDot="false"
              placeholder="输入文件名后按回车添加，也可用逗号或换行分隔"
              class="meta-ext-input limited-width-input"
            />
            <div class="form-tip">指定需要排除同步的完整名称，可填写文件夹名或文件名</div>
          </el-form-item>
          <el-form-item label="下载元数据" prop="download_meta">
            <el-radio-group v-model="form.download_meta">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.downloadMeta[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.downloadMeta[2]">是</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.downloadMeta[1]">否</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>选择“是”时，同步会下载本地缺失的元数据文件</p>
              <p>
                选择“否”时，同步不会下载缺失的元数据，<strong style="color: black"
                  >后续元数据处理也会跳过：已存在的保留，新增的不上传</strong
                >
              </p>
            </div>
          </el-form-item>
          <el-form-item label="网盘不存在的元数据" prop="upload_meta">
            <el-radio-group v-model="form.upload_meta">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.uploadMeta[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button
                :value="STRM_CUSTOM_OPTIONS.uploadMeta[3]"
                :disabled="form.download_meta === STRM_CUSTOM_OPTIONS.downloadMeta[1]"
                >删除</el-radio-button
              >
              <el-radio-button
                :value="STRM_CUSTOM_OPTIONS.uploadMeta[2]"
                :disabled="form.download_meta === STRM_CUSTOM_OPTIONS.downloadMeta[1]"
                >上传</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.uploadMeta[1]">保留</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>删除：本地存在但网盘不存在时，删除本地文件</p>
              <p>
                上传：本地存在但网盘不存在时，按以下规则处理：<br />
                &nbsp;&nbsp;&nbsp;&nbsp;1. 父目录在网盘存在则上传<br />
                &nbsp;&nbsp;&nbsp;&nbsp;2. 父目录在网盘不存在（网盘已删除）则删除本地文件<br />
                &nbsp;&nbsp;&nbsp;&nbsp;3. 父目录名属于特殊目录时，创建父目录后上传，特殊目录包括：
                <code>extrafanart</code
                >、<code>exfanarts</code>、<code>extrafanarts</code>、<code>extras</code>、<code>specials</code>、<code>shorts</code>、<code>scenes</code>、<code>featurettes</code>、<code
                  >behind the scenes</code
                >、<code>trailers</code>、<code>interviews</code>
              </p>
              <p>保留：不处理本地文件，即使网盘中已经不存在</p>
            </div>
          </el-form-item>
          <el-form-item label="检查元数据修改时间" prop="check_meta_mtime">
            <el-radio-group v-model="form.check_meta_mtime">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.checkMetaMtime[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.checkMetaMtime[2]">是</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.checkMetaMtime[1]">否</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>
                选择“是”时会比较网盘和本地文件的修改时间：<br />
                &nbsp;&nbsp;&nbsp;&nbsp;1.
                网盘文件修改时间比本地文件新，则下载网盘文件替换本地文件<br />
                &nbsp;&nbsp;&nbsp;&nbsp;2. 网盘文件修改时间比本地文件旧，则上传本地文件到网盘
              </p>
            </div>
          </el-form-item>
          <el-form-item label="网盘不存在的空目录" prop="delete_dir">
            <el-radio-group v-model="form.delete_dir">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.deleteDir[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.deleteDir[2]">删除</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.deleteDir[1]">不删除</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>同步完成后，删除本地存在但网盘不存在的空目录</p>
            </div>
          </el-form-item>
          <el-form-item label="给 STRM 链接添加路径" prop="add_path">
            <el-radio-group v-model="form.add_path" @change="updateStrmExample">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.addPath[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.addPath[1]">完整路径</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.addPath[2]">文件名</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.addPath[3]">不添加</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>
                可在 STRM 链接中附加完整原始路径或仅附加文件名，便于排查问题，也可兼容部分播放器
              </p>
            </div>
          </el-form-item>
        </template>

        <template v-if="canConfigureDirectoryUpload">
          <el-divider content-position="left">目录监控上传</el-divider>
          <el-form-item label="目录监控上传">
            <el-switch
              v-model="directoryUploadEnabled"
              :active-value="true"
              :inactive-value="false"
              :disabled="loading || directoryUploadLoading"
              @change="handleDirectoryUploadEnabledChange"
            />
            <div class="form-tip">开启后会监控本地目录，将新视频上传到当前 115 同步目录下</div>
          </el-form-item>
          <template v-if="directoryUploadEnabled">
            <div class="directory-upload-rules">
              <div
                v-for="(rule, index) in directoryUploadRules"
                :key="rule.clientId"
                class="directory-upload-rule"
              >
                <div class="directory-upload-rule__header">
                  <span class="directory-upload-rule__title">监控目录 {{ index + 1 }}</span>
                  <div class="directory-upload-rule__actions">
                    <el-switch
                      v-model="rule.enabled"
                      :active-value="true"
                      :inactive-value="false"
                      :disabled="loading || directoryUploadLoading"
                    />
                    <el-button
                      type="danger"
                      plain
                      :icon="Delete"
                      :disabled="loading || directoryUploadLoading"
                      @click="removeDirectoryUploadRule(rule.clientId)"
                    >
                      删除
                    </el-button>
                  </div>
                </div>

                <el-alert
                  v-if="getDirectoryUploadRuleGeneralError(rule)"
                  :title="getDirectoryUploadRuleGeneralError(rule)"
                  type="error"
                  show-icon
                  :closable="false"
                  class="directory-upload-rule__error"
                />

                <el-form-item
                  :label="`监控目录 ${index + 1}`"
                  :error="getDirectoryUploadRuleFieldError(rule, 'monitor_path')"
                >
                  <div class="pan-dir-input">
                    <el-input
                      v-model="rule.monitor_path"
                      placeholder="请选择需要监控上传的本地目录"
                      :disabled="loading || directoryUploadLoading || !rule.enabled"
                      readonly
                    />
                    <el-button
                      type="primary"
                      @click="openDirectoryUploadMonitorSelector(rule.clientId)"
                      :disabled="loading || directoryUploadLoading || !rule.enabled"
                    >
                      选择目录
                    </el-button>
                  </div>
                  <div class="form-tip">不要选择 STRM 存放目录，避免生成的 STRM 被再次上传</div>
                </el-form-item>

                <el-form-item
                  :label="`目标目录 ${index + 1}`"
                  :error="getDirectoryUploadRuleFieldError(rule, 'remote_root_path')"
                >
                  <div class="pan-dir-input">
                    <el-input
                      v-model="rule.remote_root_path"
                      placeholder="请选择要保存到 115 网盘的目录"
                      :disabled="loading || directoryUploadLoading || !rule.enabled"
                      readonly
                    />
                    <el-button
                      type="primary"
                      @click="openDirectoryUploadRemoteSelector(rule.clientId)"
                      :disabled="loading || directoryUploadLoading || !rule.enabled"
                    >
                      选择目录
                    </el-button>
                  </div>
                  <div class="form-help">
                    <p>
                      例如当前同步目录的远端路径是 <code>/电影</code>，这里可以选
                      <code>/电影/新片</code>。
                    </p>
                    <p>不要选 <code>/电视剧</code> 这类不在当前同步目录里的位置。</p>
                  </div>
                  <div
                    v-if="getDirectoryUploadRuleFieldError(rule, 'remote_root_id')"
                    class="form-tip form-error"
                  >
                    {{ getDirectoryUploadRuleFieldError(rule, 'remote_root_id') }}
                  </div>
                </el-form-item>

                <el-form-item
                  label="监控模式"
                  :error="getDirectoryUploadRuleFieldError(rule, 'watch_mode')"
                >
                  <el-select
                    v-model="rule.watch_mode"
                    :disabled="loading || directoryUploadLoading || !rule.enabled"
                  >
                    <el-option label="自动（推荐）" value="auto" />
                    <el-option label="性能模式" value="fsnotify" />
                    <el-option label="兼容模式" value="polling" />
                  </el-select>
                  <div class="form-tip">
                    自动（推荐）会先用性能模式尽快发现新文件，发现不到时再用兼容模式定期查漏
                  </div>
                </el-form-item>

                <el-form-item label="上传元数据">
                  <el-switch
                    v-model="rule.upload_metadata"
                    :active-value="true"
                    :inactive-value="false"
                    :disabled="loading || directoryUploadLoading || !rule.enabled"
                  />
                  <div class="form-tip">开启后会同时上传当前同步目录配置中的元数据扩展名文件</div>
                </el-form-item>

                <el-form-item
                  label="遇到同名文件时"
                  :error="getDirectoryUploadRuleFieldError(rule, 'overwrite_mode')"
                >
                  <el-radio-group
                    v-model="rule.overwrite_mode"
                    :disabled="loading || directoryUploadLoading || !rule.enabled"
                  >
                    <el-radio-button value="skip_same">跳过</el-radio-button>
                    <el-radio-button value="fail_conflict">停止</el-radio-button>
                    <el-radio-button value="replace_conflict">覆盖</el-radio-button>
                  </el-radio-group>
                  <div class="form-help">
                    <p>跳过：同名文件大小和 SHA1 一致时直接生成 STRM；不一致时不上传。</p>
                    <p>停止：同名但大小或 SHA1 不一致时停止处理。</p>
                    <p>覆盖：同名但大小或 SHA1 不一致时先删除网盘旧文件，再上传新文件。</p>
                  </div>
                </el-form-item>

                <el-form-item label="上传后删除源文件">
                  <el-switch
                    v-model="rule.delete_source_after_success"
                    :active-value="true"
                    :inactive-value="false"
                    :disabled="loading || directoryUploadLoading || !rule.enabled"
                  />
                  <div class="form-help">
                    <p>默认关闭。开启后，仅在上传成功且 STRM 生成成功后删除源文件。</p>
                    <p>删除源文件后会向上清理空目录，但不会删除监控根目录。</p>
                  </div>
                </el-form-item>
              </div>

              <el-button
                type="primary"
                plain
                :icon="Plus"
                :disabled="loading || directoryUploadLoading"
                @click="addDirectoryUploadRule"
              >
                添加监控目录
              </el-button>
            </div>
          </template>
        </template>
      </el-form>

      <div class="mobile-form-footer">
        <el-button @click="goBack">取消</el-button>
        <el-button type="primary" @click="handleSubmit" :loading="loading">
          {{ isEditMode ? '保存修改' : '确定添加' }}
        </el-button>
      </div>
    </template>

    <el-card v-else class="form-card">
      <template #header>
        <div class="card-header">
          <h3>{{ isEditMode ? '编辑同步目录' : '添加同步目录' }}</h3>
        </div>
      </template>

      <el-form
        ref="formRef"
        :model="form"
        :rules="formRules"
        label-width="160px"
        :label-position="checkIsMobile ? 'top' : 'left'"
      >
        <el-form-item
          label="同步源类型"
          prop="source_type"
          :error="getSyncPathFieldError('source_type')"
          v-if="!isEditMode"
        >
          <el-select
            v-model="form.source_type"
            placeholder="请选择同步源类型"
            @change="handleSourceTypeChange"
          >
            <el-option
              v-for="typeItem in sourceTypeOptions"
              :key="typeItem.value"
              :label="typeItem.label"
              :value="typeItem.value"
            ></el-option>
          </el-select>
          <div class="form-tip">
            <div v-if="form.source_type === 'local'">
              如果通过 CD2 挂载其他网盘，请把 CD2 的本地挂载目录映射到容器中（Docker
              部署时尤其需要），然后选择该目录
            </div>
            <div v-if="form.source_type === '115'">需要先添加用于同步的 115 账号并授权</div>
            <div v-if="form.source_type === '123'">需要先添加用于同步的 123 账号并授权</div>
          </div>
        </el-form-item>
        <el-form-item
          label="网盘账号"
          prop="account_id"
          :error="getSyncPathFieldError('account_id')"
          v-if="form.source_type !== 'local' && !isEditMode"
        >
          <el-select
            v-model="form.account_id"
            placeholder="请选择网盘账号"
            :loading="accountsLoading"
            :disabled="loading"
          >
            <el-option
              v-for="account in accounts"
              :key="account.id"
              :label="account.name"
              :value="account.id"
            ></el-option>
          </el-select>
          <div class="form-tip">选择用于同步的网盘账号</div>
        </el-form-item>
        <el-form-item
          label="来源路径"
          prop="base_cid"
          :error="getSyncPathFieldError('base_cid')"
          v-if="
            (form.source_type !== 'local' && form.account_id) ||
            form.source_type === 'local' ||
            isEditMode
          "
        >
          <div class="pan-dir-input">
            <el-input
              v-model="form.base_cid"
              placeholder="点击选择按钮选择网盘目录"
              :disabled="loading"
              readonly
            />
            <el-button type="primary" @click="openDirSelector(false)" :disabled="loading">
              选择目录
            </el-button>
          </div>
          <div v-if="selectedDirPath" class="selected-path-inline">
            <span class="path-label">选中目录路径：</span>
            <code class="path-url">{{ selectedDirPath }}</code>
          </div>
          <div class="form-tip">选择网盘中要同步的目录</div>
        </el-form-item>
        <el-form-item
          label="目标路径"
          prop="local_path"
          :error="getSyncPathFieldError('local_path')"
          v-if="
            (form.source_type !== 'local' && form.account_id) ||
            form.source_type === 'local' ||
            isEditMode
          "
        >
          <div class="pan-dir-input">
            <el-input
              v-model="form.local_path"
              placeholder="点击选择按钮选择本地目录"
              :disabled="loading"
              readonly
            />
            <el-button type="primary" @click="openDirSelector(true)" :disabled="loading">
              选择目录
            </el-button>
          </div>
          <div class="form-tip">选择本地目录作为 STRM 文件的存放位置</div>
        </el-form-item>

        <el-form-item
          label="STRM 存放目录"
          v-if="
            (form.source_type !== 'local' && form.account_id) ||
            form.source_type === 'local' ||
            isEditMode
          "
        >
          <el-input
            v-model="form.strm_path"
            placeholder="自动计算：本地目录 + 选中目录路径"
            :disabled="true"
            readonly
          />
          <div class="form-tip">STRM 和元数据实际存放目录（自动生成）</div>
        </el-form-item>

        <el-form-item label="自定义设置" prop="custom_config">
          <el-switch
            v-model="form.custom_config"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">
            开启后可自定义视频扩展名和元数据扩展名配置，否则使用 STRM 设置中的值
          </div>
        </el-form-item>

        <template v-if="form.custom_config">
          <el-form-item label="启用定时同步" prop="enable_cron">
            <el-switch
              v-model="form.enable_cron"
              :active-value="true"
              :inactive-value="false"
              :disabled="loading"
            />
            <div class="form-tip">开启后将按照定时表达式自动执行同步任务</div>
          </el-form-item>
          <el-form-item label="定时同步表达式" prop="cron">
            <el-input
              v-model="form.cron"
              placeholder="留空则使用 STRM 设置中的表达式"
              :disabled="loading"
              @blur="loadCronTimes"
            />
            <div class="form-help">
              <p><strong>常用示例：</strong></p>
              <ul class="cron-examples">
                <li><code>0 0 * * *</code> - 每天 0 点执行</li>
                <li><code>0 */6 * * *</code> - 每 6 小时执行一次</li>
                <li><code>0 2 * * *</code> - 每天凌晨 2 点执行</li>
              </ul>
              <div v-if="cronTimes.length > 0" class="cron-next-times">
                <p><strong>下 5 次执行时间：</strong></p>
                <div v-loading="cronTimesLoading" class="cron-times-list">
                  <div v-for="(time, index) in cronTimes" :key="index" class="cron-time-item">
                    <el-tag type="info" size="small">{{ time }}</el-tag>
                  </div>
                </div>
              </div>
            </div>
          </el-form-item>
          <el-form-item label="STRM 直连地址" prop="strm_base_url">
            <el-input
              v-model="form.strm_base_url"
              placeholder="留空则使用 STRM 设置中的地址"
              :disabled="loading"
              @input="updateStrmExample"
            />
            <div v-if="strmExample" class="strm-example-inline">
              <span class="example-label">示例：</span>
              <code class="example-url">{{ strmExample }}</code>
            </div>
            <div class="form-tip">
              STRM 文件将使用此地址作为基础 URL，留空则使用 STRM 设置中的值
            </div>
          </el-form-item>
          <el-form-item label="最小视频文件大小 (MB)" prop="min_video_size">
            <el-slider
              v-model="form.min_video_size"
              :min="-1"
              :max="1000"
              :step="1"
              :precision="0"
              :format-tooltip="formatTooltip"
              show-input
            />
            <div class="form-help">
              <p>
                小于此大小的视频文件将不会生成 STRM 文件，单位为 MB。设置为 0 表示不限制文件大小
              </p>
            </div>
          </el-form-item>
          <el-form-item label="视频扩展名" prop="video_ext">
            <div class="ext-input-wrapper">
              <MetadataExtInput
                v-model="form.video_ext"
                placeholder="输入扩展名后按回车添加，也可用逗号或换行分隔"
                class="meta-ext-input limited-width-input"
              />
              <el-button
                type="primary"
                link
                @click="importFromStrmSettings('video_ext')"
                :loading="importStrmSettingsLoading"
              >
                从 STRM 设置导入
              </el-button>
            </div>
            <div class="form-tip">指定需要生成 STRM 文件的视频文件扩展名</div>
          </el-form-item>
          <el-form-item label="元数据扩展名" prop="meta_ext">
            <div class="ext-input-wrapper">
              <MetadataExtInput
                v-model="form.meta_ext"
                placeholder="输入扩展名后按回车添加，也可用逗号或换行分隔"
                class="meta-ext-input limited-width-input"
              />
              <el-button
                type="primary"
                link
                @click="importFromStrmSettings('meta_ext')"
                :loading="importStrmSettingsLoading"
              >
                从 STRM 设置导入
              </el-button>
            </div>
            <div class="form-tip">指定需要同步的元数据文件扩展名</div>
          </el-form-item>
          <el-form-item label="排除文件名" prop="exclude_name">
            <MetadataExtInput
              v-model="form.exclude_name"
              :autoAddDot="false"
              placeholder="输入文件名后按回车添加，也可用逗号或换行分隔"
              class="meta-ext-input limited-width-input"
            />
            <div class="form-tip">指定需要排除同步的完整名称，可填写文件夹名或文件名</div>
          </el-form-item>
          <el-form-item label="下载元数据" prop="download_meta">
            <el-radio-group v-model="form.download_meta">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.downloadMeta[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.downloadMeta[2]">是</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.downloadMeta[1]">否</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>选择“是”时，同步会下载本地缺失的元数据文件</p>
              <p>
                选择“否”时，同步不会下载缺失的元数据，<strong style="color: black"
                  >后续元数据处理也会跳过：已存在的保留，新增的不上传</strong
                >
              </p>
            </div>
          </el-form-item>
          <el-form-item label="网盘不存在的元数据" prop="upload_meta">
            <el-radio-group v-model="form.upload_meta">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.uploadMeta[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button
                :value="STRM_CUSTOM_OPTIONS.uploadMeta[3]"
                :disabled="form.download_meta === STRM_CUSTOM_OPTIONS.downloadMeta[1]"
                >删除</el-radio-button
              >
              <el-radio-button
                :value="STRM_CUSTOM_OPTIONS.uploadMeta[2]"
                :disabled="form.download_meta === STRM_CUSTOM_OPTIONS.downloadMeta[1]"
                >上传</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.uploadMeta[1]">保留</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>删除：本地存在但网盘不存在时，删除本地文件</p>
              <p>
                上传：本地存在但网盘不存在时，按以下规则处理：<br />
                &nbsp;&nbsp;&nbsp;&nbsp;1. 父目录在网盘存在则上传<br />
                &nbsp;&nbsp;&nbsp;&nbsp;2. 父目录在网盘不存在（网盘已删除）则删除本地文件<br />
                &nbsp;&nbsp;&nbsp;&nbsp;3. 父目录名属于特殊目录时，创建父目录后上传，特殊目录包括：
                <code>extrafanart</code
                >、<code>exfanarts</code>、<code>extrafanarts</code>、<code>extras</code>、<code>specials</code>、<code>shorts</code>、<code>scenes</code>、<code>featurettes</code>、<code
                  >behind the scenes</code
                >、<code>trailers</code>、<code>interviews</code>
              </p>
              <p>保留：不处理本地文件，即使网盘中已经不存在</p>
            </div>
          </el-form-item>
          <el-form-item label="检查元数据修改时间" prop="check_meta_mtime">
            <el-radio-group v-model="form.check_meta_mtime">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.checkMetaMtime[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.checkMetaMtime[2]">是</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.checkMetaMtime[1]">否</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>
                选择“是”时会比较网盘和本地文件的修改时间：<br />
                &nbsp;&nbsp;&nbsp;&nbsp;1.
                网盘文件修改时间比本地文件新，则下载网盘文件替换本地文件<br />
                &nbsp;&nbsp;&nbsp;&nbsp;2. 网盘文件修改时间比本地文件旧，则上传本地文件到网盘
              </p>
            </div>
          </el-form-item>
          <el-form-item label="网盘不存在的空目录" prop="delete_dir">
            <el-radio-group v-model="form.delete_dir">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.deleteDir[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.deleteDir[2]">删除</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.deleteDir[1]">不删除</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>同步完成后，删除本地存在但网盘不存在的空目录</p>
            </div>
          </el-form-item>
          <el-form-item label="给 STRM 链接添加路径" prop="add_path">
            <el-radio-group v-model="form.add_path" @change="updateStrmExample">
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.addPath[0]"
                >使用 STRM 设置</el-radio-button
              >
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.addPath[1]">完整路径</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.addPath[2]">文件名</el-radio-button>
              <el-radio-button :value="STRM_CUSTOM_OPTIONS.addPath[3]">不添加</el-radio-button>
            </el-radio-group>
            <div class="form-help">
              <p>
                可在 STRM 链接中附加完整原始路径或仅附加文件名，便于排查问题，也可兼容部分播放器
              </p>
            </div>
          </el-form-item>
        </template>

        <template v-if="canConfigureDirectoryUpload">
          <el-divider content-position="left">目录监控上传</el-divider>
          <el-form-item label="目录监控上传">
            <el-switch
              v-model="directoryUploadEnabled"
              :active-value="true"
              :inactive-value="false"
              :disabled="loading || directoryUploadLoading"
              @change="handleDirectoryUploadEnabledChange"
            />
            <div class="form-tip">开启后会监控本地目录，将新视频上传到当前 115 同步目录下</div>
          </el-form-item>
          <template v-if="directoryUploadEnabled">
            <div class="directory-upload-rules">
              <div
                v-for="(rule, index) in directoryUploadRules"
                :key="rule.clientId"
                class="directory-upload-rule"
              >
                <div class="directory-upload-rule__header">
                  <span class="directory-upload-rule__title">监控目录 {{ index + 1 }}</span>
                  <div class="directory-upload-rule__actions">
                    <el-switch
                      v-model="rule.enabled"
                      :active-value="true"
                      :inactive-value="false"
                      :disabled="loading || directoryUploadLoading"
                    />
                    <el-button
                      type="danger"
                      plain
                      :icon="Delete"
                      :disabled="loading || directoryUploadLoading"
                      @click="removeDirectoryUploadRule(rule.clientId)"
                    >
                      删除
                    </el-button>
                  </div>
                </div>

                <el-alert
                  v-if="getDirectoryUploadRuleGeneralError(rule)"
                  :title="getDirectoryUploadRuleGeneralError(rule)"
                  type="error"
                  show-icon
                  :closable="false"
                  class="directory-upload-rule__error"
                />

                <el-form-item
                  :label="`监控目录 ${index + 1}`"
                  :error="getDirectoryUploadRuleFieldError(rule, 'monitor_path')"
                >
                  <div class="pan-dir-input">
                    <el-input
                      v-model="rule.monitor_path"
                      placeholder="请选择需要监控上传的本地目录"
                      :disabled="loading || directoryUploadLoading || !rule.enabled"
                      readonly
                    />
                    <el-button
                      type="primary"
                      @click="openDirectoryUploadMonitorSelector(rule.clientId)"
                      :disabled="loading || directoryUploadLoading || !rule.enabled"
                    >
                      选择目录
                    </el-button>
                  </div>
                  <div class="form-tip">不要选择 STRM 存放目录，避免生成的 STRM 被再次上传</div>
                </el-form-item>

                <el-form-item
                  :label="`目标目录 ${index + 1}`"
                  :error="getDirectoryUploadRuleFieldError(rule, 'remote_root_path')"
                >
                  <div class="pan-dir-input">
                    <el-input
                      v-model="rule.remote_root_path"
                      placeholder="请选择要保存到 115 网盘的目录"
                      :disabled="loading || directoryUploadLoading || !rule.enabled"
                      readonly
                    />
                    <el-button
                      type="primary"
                      @click="openDirectoryUploadRemoteSelector(rule.clientId)"
                      :disabled="loading || directoryUploadLoading || !rule.enabled"
                    >
                      选择目录
                    </el-button>
                  </div>
                  <div class="form-help">
                    <p>
                      例如当前同步目录的远端路径是 <code>/电影</code>，这里可以选
                      <code>/电影/新片</code>。
                    </p>
                    <p>不要选 <code>/电视剧</code> 这类不在当前同步目录里的位置。</p>
                  </div>
                  <div
                    v-if="getDirectoryUploadRuleFieldError(rule, 'remote_root_id')"
                    class="form-tip form-error"
                  >
                    {{ getDirectoryUploadRuleFieldError(rule, 'remote_root_id') }}
                  </div>
                </el-form-item>

                <el-form-item
                  label="监控模式"
                  :error="getDirectoryUploadRuleFieldError(rule, 'watch_mode')"
                >
                  <el-select
                    v-model="rule.watch_mode"
                    :disabled="loading || directoryUploadLoading || !rule.enabled"
                  >
                    <el-option label="自动（推荐）" value="auto" />
                    <el-option label="性能模式" value="fsnotify" />
                    <el-option label="兼容模式" value="polling" />
                  </el-select>
                  <div class="form-tip">
                    自动（推荐）会先用性能模式尽快发现新文件，发现不到时再用兼容模式定期查漏
                  </div>
                </el-form-item>

                <el-form-item label="上传元数据">
                  <el-switch
                    v-model="rule.upload_metadata"
                    :active-value="true"
                    :inactive-value="false"
                    :disabled="loading || directoryUploadLoading || !rule.enabled"
                  />
                  <div class="form-tip">开启后会同时上传当前同步目录配置中的元数据扩展名文件</div>
                </el-form-item>

                <el-form-item
                  label="遇到同名文件时"
                  :error="getDirectoryUploadRuleFieldError(rule, 'overwrite_mode')"
                >
                  <el-radio-group
                    v-model="rule.overwrite_mode"
                    :disabled="loading || directoryUploadLoading || !rule.enabled"
                  >
                    <el-radio-button value="skip_same">跳过</el-radio-button>
                    <el-radio-button value="fail_conflict">停止</el-radio-button>
                    <el-radio-button value="replace_conflict">覆盖</el-radio-button>
                  </el-radio-group>
                  <div class="form-help">
                    <p>跳过：同名文件大小和 SHA1 一致时直接生成 STRM；不一致时不上传。</p>
                    <p>停止：同名但大小或 SHA1 不一致时停止处理。</p>
                    <p>覆盖：同名但大小或 SHA1 不一致时先删除网盘旧文件，再上传新文件。</p>
                  </div>
                </el-form-item>

                <el-form-item label="上传后删除源文件">
                  <el-switch
                    v-model="rule.delete_source_after_success"
                    :active-value="true"
                    :inactive-value="false"
                    :disabled="loading || directoryUploadLoading || !rule.enabled"
                  />
                  <div class="form-help">
                    <p>默认关闭。开启后，仅在上传成功且 STRM 生成成功后删除源文件。</p>
                    <p>删除源文件后会向上清理空目录，但不会删除监控根目录。</p>
                  </div>
                </el-form-item>
              </div>

              <el-button
                type="primary"
                plain
                :icon="Plus"
                :disabled="loading || directoryUploadLoading"
                @click="addDirectoryUploadRule"
              >
                添加监控目录
              </el-button>
            </div>
          </template>
        </template>
      </el-form>

      <template #footer>
        <div class="form-footer">
          <el-button @click="goBack">取消</el-button>
          <el-button type="primary" @click="handleSubmit" :loading="loading">
            {{ isEditMode ? '保存修改' : '确定添加' }}
          </el-button>
        </div>
      </template>
    </el-card>

    <el-dialog
      v-model="showDirDialog"
      :title="directoryDialogTitle"
      :width="checkIsMobile ? '90%' : '600px'"
      :close-on-click-modal="false"
      body-class="directory-selector"
    >
      <div class="dir-selector">
        <DirectorySelector
          v-model="tempSelectedDir"
          :root-id="initialRootId"
          :root-path="initialRootPath"
          :source-type="selectedSourceType"
          :account-id="Number(selectedAccountId)"
          @cancel="showDirDialog = false"
          @select="confirmSelectDir"
        />
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { SERVER_URL } from '@/const'
import { STRM_CUSTOM_OPTIONS } from '@/constants/validation'
import { useHttpClient } from '@/http/client'
import { computed, onMounted, ref, reactive, watch, useTemplateRef, type Ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { ArrowLeft, Delete, Plus } from '@element-plus/icons-vue'
import { useDeviceType } from '@/composables/useDeviceType'
import { navigateBackOrReplace } from '@/utils/navigation'
import { sourceTypeOptions } from '@/utils/sourceTypeUtils'
import type { SaveSyncPathPayload } from '@/api/syncPaths'
import { useSyncDirectorySave } from '@/composables/useSyncDirectorySave'
import MetadataExtInput from './MetadataExtInput.vue'
import DirectorySelector from './DirectorySelector.vue'
import type {
  DirInfo,
  DirectoryUploadOverwriteMode,
  DirectoryUploadRule,
  DirectoryUploadWatchMode,
} from '@/typing'

interface CloudAccount {
  id: number
  name: string
  source_type: string
  user_id: string
  username: string
  created_at: number
  token: string
}

interface VersionInfo {
  version: string
  date: string
  isWindows: boolean
  isRelease: boolean
}

type DirectorySelectTarget = 'source' | 'strmLocal' | 'uploadMonitor' | 'uploadRemote'

interface DirectoryUploadRuleForm {
  clientId: number
  id: number
  autoCreated?: boolean
  enabled: boolean
  monitor_path: string
  remote_root_path: string
  remote_root_id: string
  recursive: boolean
  watch_mode: DirectoryUploadWatchMode
  upload_metadata: boolean
  startup_scan_enabled: boolean
  processed_cache_ttl_seconds: number
  delete_source_after_success: boolean
  overwrite_mode: DirectoryUploadOverwriteMode
  ignore_patterns: string[]
}

type DirectoryUploadRuleFormSeed = Partial<DirectoryUploadRule> & {
  clientId?: number
  autoCreated?: boolean
}
type DirectoryUploadRuleField =
  | 'id'
  | 'monitor_path'
  | 'remote_root_path'
  | 'remote_root_id'
  | 'watch_mode'
  | 'overwrite_mode'
  | 'rules'
type DirectoryUploadRuleFieldErrors = Partial<Record<DirectoryUploadRuleField, string>>

const http = useHttpClient()
const syncDirectorySave = useSyncDirectorySave(http)
const route = useRoute()
const router = useRouter()

const { isMobile: checkIsMobile } = useDeviceType()
const isEditMode = ref(false)
const loading = ref(false)
const createIdempotencyKey = ref(generateCreateIdempotencyKey())

const formRef = useTemplateRef<FormInstance>('formRef')
const form = reactive({
  id: 0,
  local_path: '',
  base_cid: '',
  strm_path: '',
  source_type: '',
  baidu_sync_method: 1 as 1 | 2,
  account_id: '' as string | number,
  custom_config: false,
  video_ext: [] as string[],
  meta_ext: [] as string[],
  exclude_name: [] as string[],
  remote_path: '',
  min_video_size: -1,
  upload_meta: STRM_CUSTOM_OPTIONS.uploadMeta[0] as -1 | 0 | 1 | 2,
  download_meta: STRM_CUSTOM_OPTIONS.downloadMeta[0] as -1 | 0 | 1,
  delete_dir: STRM_CUSTOM_OPTIONS.deleteDir[0] as -1 | 0 | 1,
  add_path: STRM_CUSTOM_OPTIONS.addPath[0] as -1 | 1 | 2 | 3,
  check_meta_mtime: STRM_CUSTOM_OPTIONS.checkMetaMtime[0] as -1 | 0 | 1,
  cron: '',
  enable_cron: false,
  directory_upload_enabled: false,
  strm_base_url: '',
})

function generateCreateIdempotencyKey() {
  return globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random()}`
}

const formRules: FormRules = {
  local_path: [
    { required: true, message: '请选择目标目录', trigger: 'blur' },
    { min: 1, max: 500, message: '长度在 1 到 500 个字符', trigger: 'blur' },
  ],
  base_cid: [
    { required: true, message: '请选择来源目录', trigger: 'blur' },
    { min: 1, max: 100, message: '长度在 1 到 100 个字符', trigger: 'blur' },
  ],
  source_type: [{ required: true, message: '请选择同步源类型', trigger: 'change' }],
  account_id: [{ required: true, message: '请选择网盘账号', trigger: 'change' }],
}

const accounts = ref<CloudAccount[]>([])
const accountsLoading = ref(false)

const showDirDialog = ref(false)
const selectedDirPath = ref('')
const tempSelectedDir = ref<DirInfo | null>(null)
const initialRootId = ref('')
const initialRootPath = ref('')
const directorySelectTarget = ref<DirectorySelectTarget>('source')
const directoryDialogTitle = computed(() => {
  const titles: Record<DirectorySelectTarget, string> = {
    source: '选择网盘来源目录',
    strmLocal: '选择 STRM 存放目录',
    uploadMonitor: '选择监控目录',
    uploadRemote: '选择上传目标目录',
  }
  return titles[directorySelectTarget.value]
})
const selectedSourceType = ref('')
const selectedAccountId: Ref<number | string> = ref(0)
const activeDirectoryUploadRuleClientId = ref<number | null>(null)

const versionInfo = ref<VersionInfo | null>(null)

const cronTimes = ref<string[]>([])
const cronTimesLoading = ref(false)
const strmExample = ref('')
const directoryUploadLoading = ref(false)
const directoryUploadRulesLoadFailed = ref(false)
const directoryUploadRules = ref<DirectoryUploadRuleForm[]>([])
const directoryUploadRuleFieldErrors = ref<Record<number, DirectoryUploadRuleFieldErrors>>({})
const syncPathFieldErrors = ref<Partial<Record<keyof typeof form, string>>>({})
let nextDirectoryUploadRuleClientId = 1

const canConfigureDirectoryUpload = computed(
  () => form.source_type === '115' && (isEditMode.value || !!form.account_id),
)
const directoryUploadEnabled = computed({
  get: () => form.directory_upload_enabled,
  set: (enabled: boolean) => {
    form.directory_upload_enabled = enabled
  },
})

const getSyncPathFieldError = (field: keyof typeof form): string =>
  syncPathFieldErrors.value[field] || ''

const setSyncPathFieldError = (field: string, message: string) => {
  const normalizedField = field === 'remote_path' ? 'base_cid' : field
  if (normalizedField in form) {
    syncPathFieldErrors.value[normalizedField as keyof typeof form] = message
  }
}

const formatTooltip = (value: number) => {
  if (value === STRM_CUSTOM_OPTIONS.downloadMeta[0]) {
    return '使用 STRM 设置'
  }
  return `${value} MB`
}

const createDirectoryUploadRuleForm = (
  rule: DirectoryUploadRuleFormSeed = {},
): DirectoryUploadRuleForm => ({
  clientId: nextDirectoryUploadRuleClientId++,
  id: rule.id || 0,
  autoCreated: rule.autoCreated === true,
  enabled: rule.enabled === true,
  monitor_path: rule.monitor_path || '',
  remote_root_path: rule.remote_root_path || '',
  remote_root_id: rule.remote_root_id || '',
  recursive: rule.recursive !== false,
  watch_mode: rule.watch_mode || 'auto',
  upload_metadata: rule.upload_metadata === true,
  startup_scan_enabled: rule.startup_scan_enabled !== false,
  processed_cache_ttl_seconds: rule.processed_cache_ttl_seconds || 600,
  delete_source_after_success: rule.delete_source_after_success === true,
  overwrite_mode: rule.overwrite_mode || 'skip_same',
  ignore_patterns: Array.isArray(rule.ignore_patterns) ? rule.ignore_patterns : [],
})

const resetDirectoryUploadRules = () => {
  form.directory_upload_enabled = false
  directoryUploadRulesLoadFailed.value = false
  directoryUploadRules.value = []
  directoryUploadRuleFieldErrors.value = {}
  activeDirectoryUploadRuleClientId.value = null
}

const fillDirectoryUploadRules = (rules: DirectoryUploadRule[]) => {
  directoryUploadRulesLoadFailed.value = false
  directoryUploadRules.value = rules.map((rule) => createDirectoryUploadRuleForm(rule))
  directoryUploadRuleFieldErrors.value = {}
}

const loadDirectoryUploadRules = async (syncPathId: number) => {
  if (!syncPathId || form.source_type !== '115') {
    resetDirectoryUploadRules()
    return
  }

  try {
    directoryUploadLoading.value = true
    directoryUploadRulesLoadFailed.value = false
    const response = await http.get(`${SERVER_URL}/directory-upload/rules`, {
      params: { sync_path_id: syncPathId },
    })

    if (response?.data.code === 200) {
      const rules = response.data.data?.list || []
      if (rules.length > 0) {
        fillDirectoryUploadRules(rules)
      } else {
        directoryUploadRules.value = []
        directoryUploadRuleFieldErrors.value = {}
        activeDirectoryUploadRuleClientId.value = null
      }
    } else {
      ElMessage.error(response?.data.message || '加载目录监控上传配置失败')
      directoryUploadRulesLoadFailed.value = true
    }
  } catch {
    ElMessage.error('加载目录监控上传配置失败')
    directoryUploadRulesLoadFailed.value = true
  } finally {
    directoryUploadLoading.value = false
  }
}

const trimTrailingSlash = (value: string): string => {
  if (value === '/') {
    return value
  }
  return value.replace(/\/+$/, '')
}

const normalizeLocalPathForCompare = (value: string): string => {
  let normalized = trimTrailingSlash(value.trim().replace(/\\/g, '/').replace(/\/+/g, '/'))
  if (versionInfo.value?.isWindows) {
    normalized = normalized.toLowerCase()
  }
  return normalized
}

const normalizeRemotePathForCompare = (value: string): string => {
  let normalized = value.trim().replace(/\\/g, '/').replace(/\/+/g, '/')
  if (!normalized) {
    return ''
  }
  if (!normalized.startsWith('/')) {
    normalized = `/${normalized}`
  }
  return trimTrailingSlash(normalized) || '/'
}

const isNormalizedPathInside = (path: string, parent: string): boolean => {
  if (!path || !parent || path === parent) {
    return false
  }
  if (parent === '/') {
    return path.startsWith('/')
  }
  return path.startsWith(`${parent}/`)
}

const isRemotePathWithin = (path: string, parent: string): boolean => {
  const normalizedPath = normalizeRemotePathForCompare(path)
  const normalizedParent = normalizeRemotePathForCompare(parent)
  return (
    normalizedPath === normalizedParent || isNormalizedPathInside(normalizedPath, normalizedParent)
  )
}

const hasDirectoryUploadRuleContent = (rule: DirectoryUploadRuleForm): boolean => {
  return (
    !!rule.monitor_path.trim() ||
    !!rule.remote_root_path.trim() ||
    !!rule.remote_root_id.trim() ||
    rule.id > 0
  )
}

const activeDirectoryUploadRules = () => directoryUploadRules.value.filter((rule) => rule.enabled)

const getDirectoryUploadRulesToSave = () =>
  directoryUploadRules.value.filter((rule) => rule.enabled || hasDirectoryUploadRuleContent(rule))

const addDirectoryUploadRule = (autoCreated = false) => {
  form.directory_upload_enabled = true
  directoryUploadRules.value.push(
    createDirectoryUploadRuleForm({
      autoCreated,
      enabled: true,
      remote_root_path: selectedDirPath.value || form.remote_path || '',
      remote_root_id: form.base_cid || '',
    }),
  )
}

const removeDirectoryUploadRule = (clientId: number) => {
  directoryUploadRules.value = directoryUploadRules.value.filter(
    (item) => item.clientId !== clientId,
  )
  clearDirectoryUploadRuleFieldErrors(clientId)
}

const removeCanceledDirectoryUploadDraftRules = () => {
  directoryUploadRules.value = directoryUploadRules.value.filter(
    (rule) => !(rule.id === 0 && rule.autoCreated && !rule.monitor_path.trim()),
  )
}

const getDirectoryUploadRuleFieldError = (
  rule: DirectoryUploadRuleForm,
  field: DirectoryUploadRuleField,
): string => directoryUploadRuleFieldErrors.value[rule.clientId]?.[field] || ''

const getDirectoryUploadRuleGeneralError = (rule: DirectoryUploadRuleForm): string =>
  directoryUploadRuleFieldErrors.value[rule.clientId]?.rules ||
  directoryUploadRuleFieldErrors.value[rule.clientId]?.id ||
  ''

const clearDirectoryUploadRuleFieldErrors = (clientId: number) => {
  if (!directoryUploadRuleFieldErrors.value[clientId]) {
    return
  }
  const nextErrors = { ...directoryUploadRuleFieldErrors.value }
  delete nextErrors[clientId]
  directoryUploadRuleFieldErrors.value = nextErrors
}

const clearDirectoryUploadRuleFieldError = (clientId: number, field: DirectoryUploadRuleField) => {
  const currentRuleErrors = directoryUploadRuleFieldErrors.value[clientId]
  if (!currentRuleErrors?.[field]) {
    return
  }
  const nextRuleErrors = { ...currentRuleErrors }
  delete nextRuleErrors[field]

  const nextErrors = { ...directoryUploadRuleFieldErrors.value }
  if (Object.keys(nextRuleErrors).length > 0) {
    nextErrors[clientId] = nextRuleErrors
  } else {
    delete nextErrors[clientId]
  }
  directoryUploadRuleFieldErrors.value = nextErrors
}

const setDirectoryUploadRuleFieldError = (
  clientId: number,
  field: DirectoryUploadRuleField,
  message: string,
) => {
  directoryUploadRuleFieldErrors.value = {
    ...directoryUploadRuleFieldErrors.value,
    [clientId]: {
      ...directoryUploadRuleFieldErrors.value[clientId],
      [field]: message,
    },
  }
}

const isIncompleteDirectoryUploadRule = (rule: DirectoryUploadRuleForm): boolean => {
  if (!hasDirectoryUploadRuleContent(rule)) {
    return false
  }
  return !rule.monitor_path.trim() || !rule.remote_root_path.trim() || !rule.remote_root_id.trim()
}

const markDirectoryUploadRuleRequiredFieldErrors = (rules: DirectoryUploadRuleForm[]): boolean => {
  const nextErrors: Record<number, DirectoryUploadRuleFieldErrors> = {}
  for (const rule of rules) {
    const ruleErrors: DirectoryUploadRuleFieldErrors = {}
    if (!rule.monitor_path.trim()) {
      ruleErrors.monitor_path = '请选择目录监控上传的监控目录'
    }
    if (!rule.remote_root_path.trim()) {
      ruleErrors.remote_root_path = '请选择目录监控上传的目标目录'
    }
    if (!rule.remote_root_id.trim()) {
      ruleErrors.remote_root_id = '请选择目录监控上传的目标目录'
    }
    if (Object.keys(ruleErrors).length > 0) {
      nextErrors[rule.clientId] = ruleErrors
    }
  }
  directoryUploadRuleFieldErrors.value = nextErrors
  return Object.keys(nextErrors).length === 0
}

const hasIncompleteManualDirectoryUploadDraftRules = (): boolean =>
  getDirectoryUploadRulesToSave().some(
    (rule) => rule.id === 0 && !rule.autoCreated && isIncompleteDirectoryUploadRule(rule),
  )

const hasIncompleteDirectoryUploadRulesToSave = (): boolean =>
  getDirectoryUploadRulesToSave().some((rule) => isIncompleteDirectoryUploadRule(rule))

const handleDirectoryUploadEnabledChange = (enabled: boolean | string | number) => {
  const nextEnabled = enabled === true
  if (!nextEnabled) {
    removeCanceledDirectoryUploadDraftRules()
    const requiredFieldsCompleted = markDirectoryUploadRuleRequiredFieldErrors(
      getDirectoryUploadRulesToSave(),
    )
    if (
      !requiredFieldsCompleted &&
      (hasIncompleteManualDirectoryUploadDraftRules() || hasIncompleteDirectoryUploadRulesToSave())
    ) {
      form.directory_upload_enabled = true
      ElMessage.error('请补完整目录监控上传规则，或删除未完成的规则后再关闭')
      return
    }
    form.directory_upload_enabled = false
    return
  }
  form.directory_upload_enabled = true
  if (nextEnabled) {
    if (directoryUploadRules.value.length === 0) {
      addDirectoryUploadRule(true)
    }
  }
}

const getDirectoryUploadRuleByClientId = (
  clientId: number | null,
): DirectoryUploadRuleForm | undefined => {
  if (clientId === null) {
    return undefined
  }
  return directoryUploadRules.value.find((rule) => rule.clientId === clientId)
}

const validateDirectoryUploadRuleConflicts = (rules: DirectoryUploadRuleForm[]): boolean => {
  const scopeKeys = new Set<string>()
  for (const rule of rules) {
    const monitorPath = normalizeLocalPathForCompare(rule.monitor_path)
    const remoteRootPath = normalizeRemotePathForCompare(rule.remote_root_path)
    const scopeKey = `${monitorPath}\n${remoteRootPath}\n${rule.remote_root_id.trim()}`
    if (scopeKeys.has(scopeKey)) {
      ElMessage.error('目录监控上传存在重复规则，请检查监控目录和目标目录')
      return false
    }
    scopeKeys.add(scopeKey)
  }

  const enabledRules = rules.filter((rule) => rule.enabled)
  for (let i = 0; i < enabledRules.length; i++) {
    const current = enabledRules[i]
    const currentPath = normalizeLocalPathForCompare(current.monitor_path)
    for (const other of enabledRules.slice(i + 1)) {
      const otherPath = normalizeLocalPathForCompare(other.monitor_path)
      if (currentPath === otherPath) {
        ElMessage.error('目录监控上传的监控目录不能重复')
        return false
      }
      if (current.recursive && isNormalizedPathInside(otherPath, currentPath)) {
        ElMessage.error('目录监控上传的监控目录不能位于另一个递归监控目录下')
        return false
      }
      if (other.recursive && isNormalizedPathInside(currentPath, otherPath)) {
        ElMessage.error('目录监控上传的监控目录不能位于另一个递归监控目录下')
        return false
      }
    }
  }
  return true
}

const validateDirectoryUploadRulePathBoundaries = (rules: DirectoryUploadRuleForm[]): boolean => {
  const syncRemotePath = selectedDirPath.value || form.remote_path
  const localPath = normalizeLocalPathForCompare(form.local_path)
  const strmPath = normalizeLocalPathForCompare(form.strm_path)

  for (const rule of rules) {
    const monitorPath = normalizeLocalPathForCompare(rule.monitor_path)
    if (localPath && monitorPath === localPath) {
      setDirectoryUploadRuleFieldError(
        rule.clientId,
        'monitor_path',
        '监控目录不能等于 STRM 本地目录',
      )
      ElMessage.error('目录监控上传的监控目录不能等于 STRM 本地目录')
      return false
    }
    if (strmPath && (monitorPath === strmPath || isNormalizedPathInside(monitorPath, strmPath))) {
      setDirectoryUploadRuleFieldError(
        rule.clientId,
        'monitor_path',
        '监控目录不能位于 STRM 存放目录下',
      )
      ElMessage.error('目录监控上传的监控目录不能位于 STRM 存放目录下')
      return false
    }
    if (strmPath && rule.recursive && isNormalizedPathInside(strmPath, monitorPath)) {
      setDirectoryUploadRuleFieldError(
        rule.clientId,
        'monitor_path',
        '递归监控目录不能包含 STRM 存放目录',
      )
      ElMessage.error('目录监控上传的递归监控目录不能包含 STRM 存放目录')
      return false
    }
    if (!isRemotePathWithin(rule.remote_root_path, syncRemotePath)) {
      setDirectoryUploadRuleFieldError(
        rule.clientId,
        'remote_root_path',
        '目标目录必须位于当前同步来源目录下',
      )
      ElMessage.error('目录监控上传的目标目录必须位于当前同步来源目录下')
      return false
    }
  }
  return true
}

const validateDirectoryUploadRules = (): boolean => {
  if (!canConfigureDirectoryUpload.value) {
    return true
  }
  const rulesToSave = getDirectoryUploadRulesToSave()
  const enabledRules = activeDirectoryUploadRules()
  if (!form.directory_upload_enabled && rulesToSave.length === 0) {
    return true
  }
  if (form.directory_upload_enabled && enabledRules.length === 0) {
    ElMessage.error('请至少启用一个目录监控上传规则')
    return false
  }
  if (!markDirectoryUploadRuleRequiredFieldErrors(rulesToSave)) {
    if (rulesToSave.some((rule) => !rule.monitor_path.trim())) {
      ElMessage.error('请选择目录监控上传的监控目录')
    } else {
      ElMessage.error('请选择目录监控上传的目标目录')
    }
    return false
  }
  if (!validateDirectoryUploadRulePathBoundaries(rulesToSave)) {
    return false
  }
  return validateDirectoryUploadRuleConflicts(rulesToSave)
}

const buildDirectoryUploadPayload = (rule: DirectoryUploadRuleForm) => ({
  client_id: String(rule.clientId),
  id: rule.id,
  enabled: rule.enabled,
  monitor_path: rule.monitor_path.trim(),
  remote_root_path: rule.remote_root_path.trim(),
  remote_root_id: rule.remote_root_id.trim(),
  recursive: rule.recursive,
  watch_mode: rule.watch_mode,
  upload_metadata: rule.upload_metadata,
  startup_scan_enabled: rule.startup_scan_enabled,
  processed_cache_ttl_seconds: rule.processed_cache_ttl_seconds,
  delete_source_after_success: rule.delete_source_after_success,
  ignore_patterns: rule.ignore_patterns,
  overwrite_mode: rule.overwrite_mode,
})

const buildSaveSyncPathPayload = (): SaveSyncPathPayload => {
  const rulesToSave = getDirectoryUploadRulesToSave()
  return {
    sync_path: {
      source_type: form.source_type.trim(),
      account_id: Number(form.account_id) || 0,
      base_cid: form.base_cid.trim(),
      local_path: form.local_path.trim(),
      remote_path: selectedDirPath.value,
      enable_cron: form.enable_cron,
      custom_config: form.custom_config,
      setting: {
        local_proxy: 0,
        strm_base_url: form.strm_base_url.trim(),
        cron: form.cron.trim(),
        min_video_size: form.min_video_size,
        video_ext_arr: form.video_ext,
        meta_ext_arr: form.meta_ext,
        exclude_name_arr: form.exclude_name,
        upload_meta: form.upload_meta,
        download_meta: form.download_meta,
        delete_dir: form.delete_dir,
        add_path: form.add_path,
        check_meta_mtime: form.check_meta_mtime,
      },
    },
    directory_upload: canConfigureDirectoryUpload.value
      ? {
          enabled: form.directory_upload_enabled,
          rules: rulesToSave.map(buildDirectoryUploadPayload),
        }
      : null,
  }
}

const loadCronTimes = async () => {
  if (!form.cron) {
    cronTimes.value = []
    return
  }

  try {
    cronTimesLoading.value = true
    const response = await http.get(`${SERVER_URL}/setting/cron`, {
      params: { cron: form.cron },
    })

    if (response?.data.code === 200) {
      cronTimes.value = response.data.data || []
    } else {
      cronTimes.value = []
    }
  } catch {
    cronTimes.value = []
  } finally {
    cronTimesLoading.value = false
  }
}

const importStrmSettingsLoading = ref(false)
const importFromStrmSettings = async (field: 'video_ext' | 'meta_ext') => {
  try {
    importStrmSettingsLoading.value = true
    const response = await http.get(`${SERVER_URL}/setting/strm-config`)

    if (response?.data.code === 200 && response.data.data) {
      const config = response.data.data
      if (field === 'video_ext') {
        form.video_ext = config.video_ext_arr || []
        ElMessage.success('已从 STRM 设置导入视频扩展名')
      } else {
        form.meta_ext = config.meta_ext_arr || []
        ElMessage.success('已从 STRM 设置导入元数据扩展名')
      }
    } else {
      ElMessage.error('获取 STRM 设置失败')
    }
  } catch {
    ElMessage.error('获取 STRM 设置失败')
  } finally {
    importStrmSettingsLoading.value = false
  }
}

const updateStrmExample = () => {
  if (form.strm_base_url) {
    const baseUrl = form.strm_base_url.replace(/\/$/, '')
    strmExample.value = `${baseUrl}/115/url/video.mp4?pickcode=d6tkyd62bmngxx5bg&userid=5323423`
    if (form.add_path === 1) {
      strmExample.value += '&path=Media%2F电影%2F华语电影%2F让子弹飞%2F让子弹飞.mp4'
    } else if (form.add_path === 2) {
      strmExample.value += '&path=让子弹飞.mp4'
    }
  } else {
    strmExample.value = ''
  }
}

const goBack = () => {
  void navigateBackOrReplace(router, { name: 'sync-directories' })
}

const returnToDirectoryList = () => {
  void router.replace({ name: 'sync-directories' })
}

const handleSourceTypeChange = () => {
  if (form.source_type !== 'local') {
    loadAccounts()
  }
  if (form.source_type !== '115') {
    resetDirectoryUploadRules()
  }
}

const loadAccounts = async () => {
  accounts.value = []
  try {
    accountsLoading.value = true
    const response = await http.get(`${SERVER_URL}/account/list`)
    if (response?.data.code === 200) {
      const data = response.data.data || []
      for (const account of data) {
        if (account.source_type !== form.source_type) continue
        accounts.value.push(account)
      }
    } else {
      console.error('加载账号列表失败：', response?.data.message || '未知错误')
      accounts.value = []
    }
  } catch (error) {
    console.error('加载账号列表失败：', error)
    accounts.value = []
  } finally {
    accountsLoading.value = false
  }
}

const loadVersionInfo = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/version`)
    if (response && response.data) {
      versionInfo.value = response.data
    } else {
      versionInfo.value = null
    }
  } catch (error) {
    console.error('加载系统版本信息错误：', error)
    versionInfo.value = null
  }
}

const loadDirectoryData = async (id: number) => {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/sync/path/${id}`)

    if (response?.data.code === 200) {
      const directory = response.data.data
      if (directory) {
        form.id = directory.id
        form.account_id = directory.account_id
        form.local_path = directory.local_path
        form.base_cid = directory.base_cid
        form.source_type = directory.source_type
        form.custom_config = directory.custom_config
        form.video_ext = directory.video_ext_arr || []
        form.meta_ext = directory.meta_ext_arr || []
        form.exclude_name = directory.exclude_name_arr || []
        form.remote_path = directory.remote_path
        selectedDirPath.value = directory.remote_path
        form.min_video_size = directory.min_video_size
        form.upload_meta = directory.upload_meta
        form.download_meta = directory.download_meta
        form.delete_dir = directory.delete_dir
        form.add_path = directory.add_path
        form.check_meta_mtime = directory.check_meta_mtime
        form.baidu_sync_method = directory.baidu_sync_method
        form.cron = directory.cron || ''
        form.enable_cron = directory.enable_cron !== false
        form.directory_upload_enabled = directory.directory_upload_enabled === true
        form.strm_base_url = directory.strm_base_url || ''
        updateStrmPath()
        if (form.strm_base_url) {
          updateStrmExample()
        }
        await loadDirectoryUploadRules(directory.id)
      } else {
        ElMessage.error('未找到该同步目录')
        goBack()
      }
    } else {
      ElMessage.error(response?.data.message || '加载同步目录失败')
      goBack()
    }
  } catch {
    console.error('加载同步目录错误')
    ElMessage.error('加载同步目录失败')
    goBack()
  } finally {
    loading.value = false
  }
}

const calculateStrmPath = (localPath: string, dirPath: string): string => {
  if (!localPath || !dirPath) return ''

  let cleanDirPath = dirPath
  if (versionInfo.value?.isWindows) {
    cleanDirPath = dirPath.replace(/^[/\\]+/, '').replace(/\//g, '\\')
  }
  let pathSeparator = '/'
  if (versionInfo.value?.isWindows) {
    pathSeparator = '\\'
  }
  return dirPath ? `${localPath}${pathSeparator}${cleanDirPath}` : localPath
}

const updateStrmPath = () => {
  if (form.source_type !== 'local') {
    form.strm_path = calculateStrmPath(form.local_path, selectedDirPath.value)
  } else {
    form.strm_path = form.local_path
  }
}

const openDirSelector = async (isLocalPath: boolean = false) => {
  showDirDialog.value = true
  tempSelectedDir.value = null
  activeDirectoryUploadRuleClientId.value = null
  directorySelectTarget.value = isLocalPath ? 'strmLocal' : 'source'
  selectedSourceType.value = isLocalPath ? 'local' : form.source_type
  selectedAccountId.value = form.account_id
  initialRootId.value = ''
  initialRootPath.value = ''
}

const openDirectoryUploadMonitorSelector = (clientId: number) => {
  showDirDialog.value = true
  tempSelectedDir.value = null
  activeDirectoryUploadRuleClientId.value = clientId
  directorySelectTarget.value = 'uploadMonitor'
  selectedSourceType.value = 'local'
  selectedAccountId.value = 0
  initialRootId.value = ''
  initialRootPath.value = ''
}

const openDirectoryUploadRemoteSelector = (clientId: number) => {
  showDirDialog.value = true
  tempSelectedDir.value = null
  activeDirectoryUploadRuleClientId.value = clientId
  directorySelectTarget.value = 'uploadRemote'
  selectedSourceType.value = '115'
  selectedAccountId.value = form.account_id
  initialRootId.value = form.base_cid || ''
  initialRootPath.value = selectedDirPath.value || form.remote_path || ''
}

const confirmSelectDir = async () => {
  if (!tempSelectedDir.value) return

  const selectedDir = tempSelectedDir.value

  if (directorySelectTarget.value === 'strmLocal') {
    form.local_path = selectedDir.path ? selectedDir.path : selectedDir.name
  } else if (directorySelectTarget.value === 'uploadMonitor') {
    const rule = getDirectoryUploadRuleByClientId(activeDirectoryUploadRuleClientId.value)
    if (rule) {
      rule.monitor_path = selectedDir.path ? selectedDir.path : selectedDir.name
      clearDirectoryUploadRuleFieldError(rule.clientId, 'monitor_path')
    }
  } else if (directorySelectTarget.value === 'uploadRemote') {
    const rule = getDirectoryUploadRuleByClientId(activeDirectoryUploadRuleClientId.value)
    if (rule) {
      rule.remote_root_id = selectedDir.id
      rule.remote_root_path = selectedDir.path
      clearDirectoryUploadRuleFieldError(rule.clientId, 'remote_root_path')
      clearDirectoryUploadRuleFieldError(rule.clientId, 'remote_root_id')
    }
  } else {
    form.base_cid = selectedDir.id
    selectedDirPath.value = selectedDir.path
    directoryUploadRules.value.forEach((rule) => {
      if (!rule.remote_root_path) {
        rule.remote_root_id = selectedDir.id
        rule.remote_root_path = selectedDir.path
        clearDirectoryUploadRuleFieldError(rule.clientId, 'remote_root_path')
        clearDirectoryUploadRuleFieldError(rule.clientId, 'remote_root_id')
      }
    })
    updateStrmPath()
  }

  showDirDialog.value = false
  tempSelectedDir.value = null
  directorySelectTarget.value = 'source'
  activeDirectoryUploadRuleClientId.value = null
}

const handleSubmit = async () => {
  if (!formRef.value) return

  try {
    await formRef.value.validate()
    if (directoryUploadRulesLoadFailed.value) {
      ElMessage.error('目录监控上传规则加载失败，请刷新或重试后再保存')
      return
    }
    updateStrmPath()
    if (!validateDirectoryUploadRules()) {
      return
    }
    loading.value = true

    const result = await syncDirectorySave.saveAndRun(
      isEditMode.value ? form.id : 0,
      buildSaveSyncPathPayload(),
      createIdempotencyKey.value,
      (saved) => {
        for (const warning of saved.warnings || []) {
          ElMessage.warning(warning)
        }
        ElMessage.success(isEditMode.value ? '编辑同步目录成功' : '添加同步目录成功')
        returnToDirectoryList()
      },
    )
    if (!result) {
      if (!isEditMode.value && syncDirectorySave.errorCode.value === 'IDEMPOTENCY_CONFLICT') {
        createIdempotencyKey.value = generateCreateIdempotencyKey()
      }
      directoryUploadRuleFieldErrors.value = {}
      syncPathFieldErrors.value = {}
      for (const fieldError of syncDirectorySave.fieldErrors.value) {
        if (!fieldError.client_id) {
          setSyncPathFieldError(fieldError.field, fieldError.message)
          continue
        }
        const clientId = Number(fieldError.client_id)
        if (Number.isFinite(clientId) && clientId > 0) {
          setDirectoryUploadRuleFieldError(
            clientId,
            fieldError.field as DirectoryUploadRuleField,
            fieldError.message,
          )
        }
      }
      ElMessage.error(syncDirectorySave.errorMessage.value || '保存同步目录失败')
      return
    }
  } catch {
    console.error('提交同步目录错误')
    ElMessage.error(isEditMode.value ? '编辑同步目录失败' : '添加同步目录失败')
  } finally {
    loading.value = false
  }
}

watch(
  () => form.local_path,
  () => {
    updateStrmPath()
  },
)

watch(
  () => form.cron,
  (newVal, oldVal) => {
    if (newVal !== oldVal) {
      cronTimes.value = []
    }
  },
)

onMounted(async () => {
  await loadVersionInfo()

  const id = route.params.id as string
  if (id) {
    isEditMode.value = true
    await loadDirectoryData(Number(id))
  }
})
</script>

<style scoped>
.sync-directory-form-page {
  padding: 20px;
}

.form-card {
  width: 100%;
  border-radius: 8px;
  box-shadow: 0 2px 12px 0 rgba(0, 0, 0, 0.1);
  margin-top: 16px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.card-header h3 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

.form-footer {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}

.form-tip {
  font-size: 12px;
  color: #909399;
  margin-top: 4px;
}

.form-error {
  color: #f56c6c;
}

.form-help {
  font-size: 12px;
  color: #606266;
  margin-top: 8px;
  line-height: 1.6;
}

.form-help p {
  margin: 4px 0;
}

.directory-upload-rules {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.directory-upload-rule {
  padding: 14px;
  border: 1px solid #e4e7ed;
  border-radius: 8px;
  background: #fafafa;
}

.directory-upload-rule__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.directory-upload-rule__title {
  font-size: 14px;
  font-weight: 600;
  color: #303133;
}

.directory-upload-rule__actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.directory-upload-rule__error {
  margin-bottom: 12px;
}

.pan-dir-input {
  display: flex;
  gap: 8px;
  align-items: flex-start;
}

.pan-dir-input .el-input {
  flex: 1;
}

.selected-path-inline {
  margin-top: 8px;
  padding: 8px 12px;
  background: #f5f7fa;
  border-radius: 4px;
  font-size: 12px;
}

.path-label {
  color: #909399;
  font-weight: 500;
}

.path-url {
  color: #606266;
  background: #fff;
  padding: 2px 6px;
  border-radius: 2px;
  border: 1px solid #dcdfe6;
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
}

.dir-selector {
  display: flex;
  flex-direction: column;
  gap: 16px;
  height: min(500px, calc(100dvh - 32px));
  max-height: calc(100dvh - 32px);
}

.dialog-footer {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}

.cron-examples {
  margin: 8px 0;
  padding-left: 20px;
}

.cron-examples li {
  margin: 4px 0;
  font-size: 12px;
  color: #606266;
}

.cron-examples code {
  background: #f5f7fa;
  padding: 2px 6px;
  border-radius: 3px;
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
  color: #409eff;
}

.cron-next-times {
  margin-top: 12px;
  padding-top: 8px;
  border-top: 1px dashed #e4e7ed;
}

.cron-times-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 8px;
}

.cron-time-item {
  display: inline-flex;
}

.strm-example-inline {
  margin-top: 8px;
  padding: 8px 12px;
  background: #f5f7fa;
  border-radius: 4px;
  font-size: 12px;
}

.example-label {
  color: #909399;
  font-weight: 500;
}

.example-url {
  color: #409eff;
  background: #fff;
  padding: 2px 6px;
  border-radius: 2px;
  border: 1px solid #dcdfe6;
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
  word-break: break-all;
}

.sync-directory-form-page.is-mobile {
  padding: 12px;
}

.mobile-form-header {
  margin: 12px 0;
  padding-bottom: 12px;
  border-bottom: 1px solid #ebeef5;
}

.mobile-form-header h3 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

@media (max-width: 768px) {
  .mobile-form-header {
    display: none;
  }
}

.mobile-form-footer {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid #ebeef5;
}

.is-mobile .pan-dir-input {
  flex-direction: column;
}

.is-mobile .pan-dir-input .el-button {
  width: 100%;
}

.is-mobile .directory-upload-rule__header {
  align-items: flex-start;
  flex-direction: column;
}

.is-mobile .directory-upload-rule__actions {
  width: 100%;
  justify-content: space-between;
}

.ext-input-wrapper {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  flex-wrap: wrap;
}

.ext-input-wrapper .meta-ext-input {
  flex: 1;
  min-width: 200px;
}

.ext-input-wrapper .el-button {
  flex-shrink: 0;
}
</style>
