<template>
  <div class="main-content-container strm-content">
    <PageHeader />

    <!-- STRM 设置卡片 -->
    <el-form
      :model="strmData"
      :rules="formRules"
      :label-position="checkIsMobile ? 'top' : 'left'"
      :label-width="180"
      class="strm-form"
      ref="formRef"
    >
      <!-- 排除的名称 -->
      <el-form-item label="排除的名称" prop="exclude_name_arr">
        <MetadataExtInput
          v-model="strmData.exclude_name_arr"
          placeholder="输入名称后按回车添加"
          class="meta-ext-input limited-width-input"
          :autoAddDot="false"
        />
        <div class="form-help">
          <p>指定需要排除的文件名或目录名，完整匹配不支持正则表达式。</p>
          <p>被排除的文件或目录将不会同步，其下的所有内容也都不会同步</p>
        </div>
      </el-form-item>
      <!-- 视频文件扩展名 -->
      <el-form-item label="视频文件扩展名" prop="video_ext_arr">
        <MetadataExtInput
          v-model="strmData.video_ext_arr"
          placeholder="输入扩展名后按回车添加，也可用逗号或换行分隔"
          class="meta-ext-input limited-width-input"
        />
        <div class="form-help">
          <p>指定需要生成 STRM 文件的视频文件扩展名，如：.mp4、.mkv、.avi、.mov 等</p>
        </div>
      </el-form-item>

      <!-- 最小文件大小 -->
      <el-form-item label="最小文件大小 (MB)" prop="min_video_size">
        <el-input-number
          v-model="strmData.min_video_size"
          :min="0"
          :step="1"
          :precision="0"
          placeholder="输入最小文件大小"
          :disabled="strmLoading"
          class="limited-width-input"
        />
        <div class="form-help">
          <p>小于此大小的视频文件将不会生成 STRM 文件，单位为 MB。设置为 0 表示不限制文件大小</p>
        </div>
      </el-form-item>

      <!-- 元数据扩展名 -->
      <el-form-item label="元数据扩展名" prop="meta_ext_arr">
        <MetadataExtInput
          v-model="strmData.meta_ext_arr"
          placeholder="输入扩展名后按回车添加，也可用逗号或换行分隔"
          class="meta-ext-input limited-width-input"
        />
        <div class="form-help">
          <p>指定需要处理的元数据文件扩展名，如：.jpg、.nfo、.srt、.ass 等</p>
        </div>
      </el-form-item>

      <!-- 定时同步表达式 -->
      <el-form-item label="定时同步表达式" prop="cron">
        <el-input
          v-model="strmData.cron"
          placeholder="输入 Cron 表达式，如：0 2 * * *"
          :disabled="strmLoading"
          class="limited-width-input"
          @blur="loadCronTimes"
        />
        <div class="form-help">
          <p><strong>常用示例：</strong></p>
          <ul class="cron-examples">
            <li><code>0 0 * * *</code> - 每天 0 点执行</li>
            <li><code>0 */6 * * *</code> - 每 6 小时执行一次</li>
            <li><code>0 2 * * *</code> - 每天凌晨 2 点执行</li>
            <li><code>0 0 * * 0</code> - 每周日 0 点执行</li>
          </ul>

          <!-- 下次执行时间显示 -->
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

      <!-- STRM 直连地址 -->
      <el-form-item label="STRM 直连地址" prop="direct_url">
        <el-input
          v-model="strmData.strm_base_url"
          placeholder="输入 HTTP 地址，如：http://192.168.1.100:8080"
          :disabled="strmLoading"
          @input="updateStrmExample"
          class="limited-width-input"
        />
        <div v-if="strmExample" class="strm-example-inline">
          <span class="example-label">示例：</span>
          <code class="example-url">{{ strmExample }}</code>
        </div>
        <div class="form-help">
          <p>STRM 文件将使用此地址作为基础 URL，请确保媒体服务器可以访问此地址</p>
          <p>通常填写部署 QMediaSync 机器的 IP 和端口，如：http://192.168.1.100:12333</p>
        </div>
      </el-form-item>

      <el-form-item label="下载元数据" prop="download_meta">
        <el-radio-group v-model="strmData.download_meta" @change="changeDownloadMeta">
          <el-radio-button :value="1">是</el-radio-button>
          <el-radio-button :value="0">否</el-radio-button>
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

      <!-- 同步完是否上传网盘不存在的元数据 -->
      <el-form-item label="网盘不存在的元数据" prop="upload_meta">
        <el-radio-group v-model="strmData.upload_meta">
          <el-radio-button :value="2" :disabled="strmData.download_meta === 0"
            >删除</el-radio-button
          >
          <el-radio-button :value="1" :disabled="strmData.download_meta === 0"
            >上传</el-radio-button
          >
          <el-radio-button :value="0">保留</el-radio-button>
        </el-radio-group>
        <div class="form-help">
          <p>删除：本地存在但网盘不存在时，删除本地文件</p>
          <p>
            上传：本地存在但网盘不存在时，按以下规则处理：<br />
            &nbsp;&nbsp;&nbsp;&nbsp;1. 父目录在网盘存在则上传<br />
            &nbsp;&nbsp;&nbsp;&nbsp;2. 父目录在网盘不存在（网盘已删除）则删除本地文件<br />
            &nbsp;&nbsp;&nbsp;&nbsp;3.
            父目录是特定名字，则创建父目录并上传，特定名字包括："extrafanart", "exfanarts",
            "extrafanarts", "extras", "specials", "shorts", "scenes", "featurettes", "behind the
            scenes", "trailers", "interviews",
          </p>
          <p>保留：不处理本地文件，即使网盘中已经不存在</p>
        </div>
      </el-form-item>
      <el-form-item label="检查元数据修改时间" prop="check_meta_mtime">
        <el-radio-group v-model="strmData.check_meta_mtime">
          <el-radio-button :value="1">是</el-radio-button>
          <el-radio-button :value="0">否</el-radio-button>
        </el-radio-group>
        <div class="form-help">
          <p>
            选择“是”时会比较网盘和本地文件的修改时间：<br />
            &nbsp;&nbsp;&nbsp;&nbsp;1. 网盘文件修改时间比本地文件新，则下载网盘文件替换本地文件<br />
            &nbsp;&nbsp;&nbsp;&nbsp;2. 网盘文件修改时间比本地文件旧，则上传本地文件到网盘
          </p>
        </div>
      </el-form-item>
      <!-- 同步完是否删除网盘不存在的空目录 -->
      <el-form-item label="网盘不存在的空目录" prop="delete_dir">
        <el-radio-group v-model="strmData.delete_dir">
          <el-radio-button :value="1">删除</el-radio-button>
          <el-radio-button :value="0">不删除</el-radio-button>
        </el-radio-group>
        <div class="form-help">
          <p>同步完成后，删除本地存在但网盘不存在的空目录</p>
        </div>
      </el-form-item>

      <el-form-item label="给 STRM 链接添加路径" prop="add_path">
        <el-radio-group v-model="strmData.add_path" @change="updateStrmExample">
          <el-radio-button :value="1">完整路径</el-radio-button>
          <el-radio-button :value="2">文件名</el-radio-button>
          <el-radio-button :value="3">不添加</el-radio-button>
        </el-radio-group>
        <div class="form-help">
          <p>可在 STRM 链接中附加完整原始路径或仅附加文件名，便于排查问题，也可兼容部分播放器</p>
        </div>
      </el-form-item>

      <el-form-item label="启用本地代理播放" prop="local_proxy">
        <el-radio-group v-model="strmData.local_proxy">
          <el-radio-button :value="1">启用</el-radio-button>
          <el-radio-button :value="0">关闭</el-radio-button>
        </el-radio-group>
        <div class="form-help">
          <p>
            如果使用本项目的 Emby 代理 8095
            端口播放，除百度网盘外，其他网盘不受此开关影响（等同于关闭）。
          </p>
          <p>
            如果使用 Emby 8096 端口播放，开启后流量会经过 QMediaSync 代理，可解决部分播放器因 UA
            不一致导致无法播放的问题。
          </p>
          <p>
            百度网盘默认不支持 302。如果使用 8095 播放，且播放器不支持百度网盘 302，请开启此开关。
          </p>
        </div>
      </el-form-item>

      <!-- 保存和重置按钮 -->
      <div class="strm-actions">
        <el-button
          type="success"
          @click="saveStrmConfig"
          :loading="strmLoading"
          size="large"
          :icon="Check"
        >
          保存 STRM 配置
        </el-button>
      </div>

      <!-- STRM 配置状态显示 -->
      <el-alert
        v-if="strmStatus"
        :title="strmStatus.title"
        :type="strmStatus.type"
        :description="strmStatus.description"
        :closable="false"
        show-icon
        class="strm-status"
      />

      <el-divider content-position="left">STRM Webhook</el-divider>
      <el-alert
        type="warning"
        show-icon
        :closable="false"
        class="webhook-warning"
        title="STRM Webhook 仍处于测试阶段"
        description="当前仅支持 115 网盘，接口字段和处理行为后续可能调整。请求字段、示例和边界说明请查看正式文档，建议先在少量目录验证后再接入自动化流程。"
      />
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { Check } from '@element-plus/icons-vue'
import { onMounted, reactive, ref, watch, useTemplateRef } from 'vue'
import type { FormInstance, FormRules } from 'element-plus'
import { isMobile } from '@/utils/deviceUtils'
import PageHeader from '@/components/common/PageHeader.vue'
import MetadataExtInput from './MetadataExtInput.vue'
interface StrmData {
  video_ext_arr: string[]
  min_video_size: number
  meta_ext_arr: string[]
  cron: string
  strm_base_url: string
  upload_meta: 0 | 1 | 2
  download_meta: 0 | 1
  delete_dir: 0 | 1
  local_proxy: 0 | 1
  exclude_name_arr: string[]
  add_path: 1 | 2 | 3
  check_meta_mtime: 0 | 1
}

interface StrmStatus {
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
}
const checkIsMobile = ref(isMobile())
const http = useHttpClient()

// 表单引用
const formRef = useTemplateRef<FormInstance>('formRef')

// STRM 配置相关状态
const strmLoading = ref(false)
const strmStatus = ref<StrmStatus | null>(null)
const strmExample = ref('')

// Cron 下次执行时间相关状态
const cronTimes = ref<string[]>([])
const cronTimesLoading = ref(false)

// 默认 STRM 配置
const defaultStrmData: StrmData = {
  video_ext_arr: ['.mp4', '.mkv', '.avi', '.mov', '.wmv', '.flv', '.webm', '.m4v', '.3gp', '.ts'],
  min_video_size: 50, // 默认 50 MB
  meta_ext_arr: ['.jpg', '.jpeg', '.png', '.webp', '.nfo', '.srt', '.ass', '.svg', '.sup', '.lrc'],
  cron: '30 * * * *',
  strm_base_url: '',
  upload_meta: 0, // 默认保留
  download_meta: 1, // 默认下载元数据
  delete_dir: 0, // 默认不删除
  local_proxy: 0, // 是否启用本地代理
  exclude_name_arr: [], // 排除的名称列表，默认为空
  add_path: 3, // 默认不添加路径
  check_meta_mtime: 0, // 检查元数据的修改时间
}

const strmData = reactive<StrmData>({ ...defaultStrmData })

// 表单验证规则
const formRules: FormRules = {
  video_ext_arr: [
    {
      required: true,
      validator: (rule, value, callback) => {
        if (!value || value.length === 0) {
          callback(new Error('请至少添加一个视频文件扩展名'))
        } else {
          callback()
        }
      },
      trigger: 'change',
    },
  ],
  min_video_size: [
    { required: true, message: '请输入最小文件大小', trigger: 'blur' },
    {
      validator: (rule, value, callback) => {
        if (value < 0) {
          callback(new Error('文件大小不能小于 0'))
        } else {
          callback()
        }
      },
      trigger: 'blur',
    },
  ],
  meta_ext_arr: [
    {
      required: true,
      validator: (rule, value, callback) => {
        if (!value || value.length === 0) {
          callback(new Error('请至少添加一个元数据扩展名'))
        } else {
          callback()
        }
      },
      trigger: 'change',
    },
  ],
  cron: [{ required: true, message: '请输入定时同步表达式', trigger: 'blur' }],
  strm_base_url: [
    { required: true, message: '请输入 STRM 直连地址', trigger: 'blur' },
    {
      pattern: /^https?:\/\/.+/,
      message: '请输入有效的 HTTP 或 HTTPS 地址',
      trigger: 'blur',
    },
  ],
}

// 更新 STRM 示例
const updateStrmExample = () => {
  if (strmData.strm_base_url) {
    // 生成示例 STRM 文件内容
    const baseUrl = strmData.strm_base_url.replace(/\/$/, '') // 移除末尾斜杠
    strmExample.value = `${baseUrl}/115/url/video.mp4?pickcode=d6tkyd62bmngxx5bg&userid=5323423`
    if (strmData.add_path === 1) {
      strmExample.value += '&path=Media%2F电影%2F华语电影%2F让子弹飞%2F让子弹飞.mp4'
    } else if (strmData.add_path === 2) {
      strmExample.value += '&path=让子弹飞.mp4'
    }
  } else {
    strmExample.value = ''
  }
}

// 保存 STRM 配置
const saveStrmConfig = async () => {
  // 验证表单
  if (!formRef.value) return

  try {
    await formRef.value.validate()
  } catch (error) {
    console.warn('表单验证失败：', error)
    return
  }

  try {
    strmLoading.value = true
    strmStatus.value = null

    const response = await http.post(`${SERVER_URL}/setting/strm-config`, strmData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      strmStatus.value = {
        title: 'STRM 配置已保存',
        type: 'success',
        description: '所有 STRM 相关设置已成功保存，将在下次同步时生效',
      }
    } else {
      strmStatus.value = {
        title: '保存 STRM 配置失败',
        type: 'error',
        description: response?.data.message || '保存设置失败，请重试',
      }
    }
  } catch (error) {
    console.error('保存 STRM 配置错误：', error)
    strmStatus.value = {
      title: '保存设置出错',
      type: 'error',
      description: '保存过程中发生错误，请检查网络连接',
    }
  } finally {
    strmLoading.value = false
  }
}

// 加载 STRM 配置
const loadStrmConfig = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/setting/strm-config`)

    if (response?.data.code === 200 && response.data.data) {
      const config = response.data.data
      strmData.video_ext_arr = config.video_ext_arr
      strmData.min_video_size = config.min_video_size
      strmData.meta_ext_arr = config.meta_ext_arr
      strmData.cron = config.cron
      strmData.strm_base_url = config.strm_base_url
      strmData.download_meta = config.download_meta
      strmData.upload_meta = config.upload_meta
      strmData.delete_dir = config.delete_dir
      strmData.local_proxy = config.local_proxy
      strmData.exclude_name_arr = config.exclude_name_arr
      strmData.add_path = config.add_path
      strmData.check_meta_mtime = config.check_meta_mtime

      // 更新示例
      updateStrmExample()

      // 加载 Cron 执行时间
      await loadCronTimes()
    }
  } catch (error) {
    console.error('加载 STRM 配置错误：', error)
  }
}

// 查询 Cron 下次执行时间
const loadCronTimes = async () => {
  if (!strmData.cron) {
    cronTimes.value = []
    return
  }

  try {
    cronTimesLoading.value = true
    const response = await http.get(`${SERVER_URL}/setting/cron`, {
      params: { cron: strmData.cron },
    })

    if (response?.data.code === 200 && response.data.data) {
      cronTimes.value = response.data.data || []
    } else {
      cronTimes.value = []
    }
  } catch (error) {
    console.error('查询 Cron 执行时间错误：', error)
    cronTimes.value = []
  } finally {
    cronTimesLoading.value = false
  }
}

const changeDownloadMeta = () => {
  if (strmData.download_meta === 0) {
    strmData.upload_meta = 0
  }
}

// 监听 Cron 表达式变化
watch(
  () => strmData.cron,
  (newCron) => {
    if (newCron && newCron.trim()) {
      loadCronTimes()
    } else {
      cronTimes.value = []
    }
  },
  { immediate: false },
)

onMounted(() => {
  loadStrmConfig()
})
</script>

<style scoped>
.webhook-warning {
  margin-bottom: 18px;
}
</style>
