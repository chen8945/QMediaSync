<template>
  <div class="main-content-container category-strategy-container full-width-container">
    <PageHeader />

    <el-tabs v-model="activeTab" @tab-change="handleTabChange" type="card">
      <!-- 电影分类 tab -->
      <el-tab-pane label="电影" name="movie">
        <div class="tab-content">
          <div class="card-actions-header">
            <el-button type="primary" :icon="Plus" @click="handleAdd('movie')" class="add-button">
              添加分类
            </el-button>
          </div>

          <div class="cards-container">
            <el-card shadow="hover" v-for="row in movieCategories" :key="row.id">
              <template #header>
                <div class="card-header">
                  <div class="card-title">
                    <el-tooltip class="box-item" :content="'ID：' + row.id" placement="bottom">
                      #{{ row.id }} {{ row.name }}
                    </el-tooltip>
                  </div>
                </div>
              </template>

              <div class="card-body">
                <div class="info-item">
                  <span class="info-label">语言：</span>
                  <span class="info-value">
                    {{
                      row.language_array && row.language_array.length > 0
                        ? row.language_array.map((l: string) => getLanguageName(l)).join(', ')
                        : '全部语言'
                    }}
                  </span>
                </div>
                <div class="info-item">
                  <span class="info-label">类别：</span>
                  <span class="info-value">
                    {{
                      row.genre_id_array && row.genre_id_array.length > 0
                        ? row.genre_id_array.map((g: number) => getMovieGenreName(g)).join(', ')
                        : '全部分类'
                    }}
                  </span>
                </div>
                <div class="info-item">
                  <span class="info-label">创建时间：</span>
                  <span class="info-value">{{
                    formatDateTime(row.created_at ? row.created_at : 0)
                  }}</span>
                </div>
              </div>

              <template #footer>
                <div class="card-actions">
                  <span v-if="row.id === 1" class="text-muted">系统预设</span>
                  <template v-else>
                    <el-button
                      type="primary"
                      size="small"
                      :icon="Edit"
                      @click="handleEdit('movie', row)"
                    >
                      编辑
                    </el-button>
                    <el-button
                      type="danger"
                      size="small"
                      :icon="Delete"
                      @click="handleDelete('movie', row.id)"
                    >
                      删除
                    </el-button>
                  </template>
                </div>
              </template>
            </el-card>

            <el-col v-if="movieCategories.length === 0" :span="24" class="empty-card-col">
              <el-card shadow="never" class="empty-card">
                <div class="empty-content">
                  <el-icon class="empty-icon">
                    <Folder />
                  </el-icon>
                  <p class="empty-text">暂无电影分类策略</p>
                </div>
              </el-card>
            </el-col>
          </div>
        </div>
      </el-tab-pane>

      <!-- 电视剧分类 tab -->
      <el-tab-pane label="电视剧" name="tvshow">
        <div class="tab-content">
          <div class="card-actions-header">
            <el-button type="primary" :icon="Plus" @click="handleAdd('tvshow')" class="add-button">
              添加分类
            </el-button>
          </div>

          <div class="cards-container">
            <el-card shadow="hover" v-for="row in tvshowCategories" :key="row.id">
              <template #header>
                <div class="card-header">
                  <div class="card-title">
                    <el-tooltip class="box-item" :content="'ID：' + row.id" placement="bottom">
                      #{{ row.id }} {{ row.name }}
                    </el-tooltip>
                  </div>
                </div>
              </template>

              <div class="card-body">
                <div class="info-item">
                  <span class="info-label">国家或地区：</span>
                  <span class="info-value">
                    {{
                      row.country_array && row.country_array.length > 0
                        ? row.country_array.map((c: string) => getCountryName(c)).join(', ')
                        : '全部国家'
                    }}
                  </span>
                </div>
                <div class="info-item">
                  <span class="info-label">类别：</span>
                  <span class="info-value">
                    {{
                      row.genre_id_array && row.genre_id_array.length > 0
                        ? row.genre_id_array.map((g: number) => getTvshowGenreName(g)).join(', ')
                        : '全部分类'
                    }}
                  </span>
                </div>
                <div class="info-item">
                  <span class="info-label">创建时间：</span>
                  <span class="info-value">{{
                    formatDateTime(row.created_at ? row.created_at : 0)
                  }}</span>
                </div>
              </div>

              <template #footer>
                <div class="card-actions">
                  <span v-if="row.id === 1" class="text-muted">系统预设</span>
                  <template v-else>
                    <el-button
                      type="primary"
                      size="small"
                      :icon="Edit"
                      @click="handleEdit('tvshow', row)"
                    >
                      编辑
                    </el-button>
                    <el-button
                      type="danger"
                      size="small"
                      :icon="Delete"
                      @click="handleDelete('tvshow', row.id)"
                    >
                      删除
                    </el-button>
                  </template>
                </div>
              </template>
            </el-card>

            <el-col v-if="tvshowCategories.length === 0" :span="24" class="empty-card-col">
              <el-card shadow="never" class="empty-card">
                <div class="empty-content">
                  <el-icon class="empty-icon">
                    <Folder />
                  </el-icon>
                  <p class="empty-text">暂无电视剧分类策略</p>
                </div>
              </el-card>
            </el-col>
          </div>
        </div>
      </el-tab-pane>
    </el-tabs>

    <!-- 添加/编辑弹窗 -->
    <el-dialog
      v-model="dialogVisible"
      :title="dialogType === 'add' ? '添加分类' : '编辑分类'"
      width="500px"
      @close="handleDialogClose"
    >
      <el-form ref="formRef" :model="formData" :rules="formRules" label-width="100px">
        <el-form-item label="分类名称" prop="name">
          <el-input v-model="formData.name" placeholder="请输入分类名称" />
        </el-form-item>

        <el-form-item :label="editingType === 'movie' ? '语言' : '国家'" prop="languages">
          <el-select
            v-model="formData.languages"
            multiple
            collapse-tags
            filterable
            :placeholder="
              editingType === 'movie'
                ? '请输入语言名称搜索或选择（多选）'
                : '请输入国家名称搜索或选择（多选）'
            "
            style="width: 100%"
          >
            <el-option
              v-for="item in editingType === 'movie' ? languages : countries"
              :key="item.code"
              :label="item.name"
              :value="item.code"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="类别" prop="genres">
          <el-select
            v-model="formData.genres"
            multiple
            collapse-tags
            filterable
            placeholder="请输入类别名称搜索或选择（多选）"
            style="width: 100%"
          >
            <el-option
              v-for="genre in currentGenres"
              :key="genre.id"
              :label="genre.name"
              :value="genre.id"
            />
          </el-select>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSubmit">确认</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, useTemplateRef } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { Plus, Edit, Delete, Folder } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'

// 响应式数据
const activeTab = ref('movie')
const dialogVisible = ref(false)
const dialogType = ref<'add' | 'edit'>('add')
const editingId = ref<number | null>(null)
const editingType = ref<'movie' | 'tvshow'>('movie')
const formRef = useTemplateRef<FormInstance>('formRef')
const http = useHttpClient()

interface category {
  id: number
  name: string
  language_array?: string[]
  country_array?: string[]
  genre_id_array: number[]
  created_at?: number
}

// 时间格式化函数
const formatDateTime = (timestamp: number) => {
  if (!timestamp) return ''
  const date = new Date(timestamp * 1000) // 转换为毫秒
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const hours = String(date.getHours()).padStart(2, '0')
  const minutes = String(date.getMinutes()).padStart(2, '0')
  const seconds = String(date.getSeconds()).padStart(2, '0')
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`
}

const movieCategories = ref<category[]>([])
const tvshowCategories = ref<category[]>([])

// 语言、国家和类别数据
const languages = ref<{ code: string; name: string }[]>([])
const countries = ref<{ code: string; name: string }[]>([])
const movieGenres = ref<{ id: number; name: string }[]>([])
const tvshowGenres = ref<{ id: number; name: string }[]>([])

// 表单数据
const formData = reactive({
  id: 0,
  name: '',
  languages: [] as string[],
  genres: [] as number[],
})

// 表单验证规则
const formRules = {
  name: [{ required: true, message: '请输入分类名称', trigger: 'blur' }],
}

// 计算属性
const currentGenres = computed(() => {
  return activeTab.value === 'movie' ? movieGenres.value : tvshowGenres.value
})

// 方法
const getLanguageName = (code: string) => {
  const lang = languages.value.find((l) => l.code === code.toLowerCase())
  return lang ? lang.name : code
}

const getCountryName = (code: string) => {
  const country = countries.value.find((c) => c.code === code)
  return country ? country.name : code
}

const getMovieGenreName = (id: number): string => {
  const genre = movieGenres.value.find((g) => g.id === id)
  return genre ? genre.name : id.toString()
}

const getTvshowGenreName = (id: number): string => {
  const genre = tvshowGenres.value.find((g) => g.id === id)
  return genre ? genre.name : id.toString()
}

const handleTabChange = () => {
  loadCategories()
}

const loadLanguages = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/scrape/language`)
    if (response?.data?.code === 200) {
      languages.value = response.data.data || []
    }
  } catch (error) {
    console.error('加载语言列表失败：', error)
    ElMessage.error('加载语言列表失败')
  }
}

const loadCountries = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/scrape/countries`)
    if (response?.data?.code === 200) {
      countries.value = response.data.data || []
    }
  } catch (error) {
    console.error('加载国家列表失败：', error)
    ElMessage.error('加载国家列表失败')
  }
}

const loadMovieGenres = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/scrape/movie-genre`)
    if (response?.data?.code === 200) {
      movieGenres.value = response.data.data || []
    }
  } catch (error) {
    console.error('加载电影类别失败：', error)
    ElMessage.error('加载电影类别失败')
  }
}

const loadTvshowGenres = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/scrape/tvshow-genre`)
    if (response?.data?.code === 200) {
      tvshowGenres.value = response.data.data || []
    }
  } catch (error) {
    console.error('加载电视剧类别失败：', error)
    ElMessage.error('加载电视剧类别失败')
  }
}

const loadCategories = async () => {
  try {
    const type = activeTab.value
    const response = await http.get(`${SERVER_URL}/scrape/${type}-categories`)
    if (response?.data?.code === 200) {
      // 转换数据结构以匹配表格展示需求
      const categories = response.data.data.map((item: category) => ({
        id: item.id,
        name: item.name,
        language_array: item.language_array || [],
        country_array: item.country_array || [],
        genre_id_array: item.genre_id_array || [],
        created_at: item.created_at,
      }))

      if (type === 'movie') {
        movieCategories.value = categories
      } else {
        tvshowCategories.value = categories
      }
    } else {
      ElMessage.error(`加载分类列表失败：${response?.data?.msg || '未知错误'}`)
    }
  } catch (error) {
    console.error('加载分类列表异常：', error)
    ElMessage.error('加载分类列表异常')
  }
}

const handleAdd = (type: 'movie' | 'tvshow') => {
  dialogType.value = 'add'
  editingType.value = type
  editingId.value = null
  resetForm()
  dialogVisible.value = true
}

const handleEdit = (type: 'movie' | 'tvshow', row: category) => {
  dialogType.value = 'edit'
  editingType.value = type
  editingId.value = row.id

  // 填充表单数据
  formData.id = row.id
  formData.name = row.name
  // 根据类型使用不同的数组字段
  if (type === 'movie') {
    formData.languages = [...(row.language_array || [])]
  } else {
    formData.languages = [...(row.country_array || [])]
  }
  formData.genres = [...(row.genre_id_array || [])]

  dialogVisible.value = true
}

const handleDelete = async (type: 'movie' | 'tvshow', id: number) => {
  try {
    await ElMessageBox.confirm('确定要删除该分类吗？', '确认删除', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning',
    })

    const response = await http.delete(`${SERVER_URL}/scrape/${type}-categories/${id}`)
    if (response?.data?.code === 200) {
      ElMessage.success('删除成功')
      loadCategories()
    } else {
      ElMessage.error('删除失败：' + (response?.data?.msg || '未知错误'))
    }
  } catch (error: unknown) {
    if (error !== 'cancel') {
      console.error('删除分类失败：', error)
      ElMessage.error('删除失败')
    }
  }
}

const resetForm = () => {
  formData.id = 0
  formData.name = ''
  formData.languages = []
  formData.genres = []
  if (formRef.value) {
    formRef.value.resetFields()
  }
}

const handleDialogClose = () => {
  resetForm()
}

const handleSubmit = async () => {
  try {
    // 验证表单
    await formRef.value?.validate()

    // 根据后端需要的数据结构格式化 payload
    const payload: category = {
      id: 0,
      name: formData.name,
      genre_id_array: formData.genres,
    }

    // 根据类型添加不同的数组字段
    if (editingType.value === 'movie') {
      payload.language_array = formData.languages
    } else {
      payload.country_array = formData.languages
    }

    let response
    if (dialogType.value === 'add') {
      response = await http.post(`${SERVER_URL}/scrape/${editingType.value}-categories`, payload)
    } else {
      payload.id = formData.id
      response = await http.post(`${SERVER_URL}/scrape/${editingType.value}-categories`, payload)
    }

    if (response?.data?.code === 200) {
      ElMessage.success(dialogType.value === 'add' ? '添加成功' : '编辑成功')
      dialogVisible.value = false
      loadCategories()
    } else {
      ElMessage.error(
        `${dialogType.value === 'add' ? '添加失败' : '编辑失败'}：${response?.data?.msg || '未知错误'}`,
      )
    }
  } catch (error: unknown) {
    if (error !== false) {
      console.error('提交表单失败：', error)
    }
  }
}

// 初始化
onMounted(async () => {
  await Promise.all([
    loadLanguages(),
    loadCountries(),
    loadMovieGenres(),
    loadTvshowGenres(),
    loadCategories(),
  ])
})
</script>

<style scoped>
.full-width-container {
  padding: 0;
}

.category-strategy-container.full-width-container {
  margin-top: 0 !important;
  padding-top: 20px !important;
}

.full-width-card {
  border-radius: 0;
  box-shadow: none;
}

.category-strategy-container {
  border: 0;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.cards-container {
  display: flex;
  flex-wrap: wrap;
  gap: 20px;
  margin-top: 20px;
}

.cards-container :deep(.el-card) {
  flex: 1;
  min-width: 320px;
  max-width: 320px;
  transition:
    border-color 0.2s ease,
    box-shadow 0.2s ease;
}

.card-actions-header {
  margin-bottom: 20px;
}

.card-body {
  padding: 10px 0;
  height: 180px;
}

.info-item {
  display: flex;
  align-items: flex-start;
  margin-bottom: 12px;
}

.info-item:last-child {
  margin-bottom: 0;
}

.info-label {
  font-weight: 500;
  margin-right: 8px;
  min-width: 60px;
  white-space: nowrap;
}

.info-value {
  flex: 1;
  word-break: break-word;
}

.card-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}

.empty-card-col {
  width: 100%;
}

.empty-card {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 200px;
  border: 1px dashed #dcdfe6;
  background-color: #f5f7fa;
}

.empty-content {
  text-align: center;
  color: #909399;
}

.empty-icon {
  font-size: 48px;
  margin-bottom: 16px;
}

.empty-text {
  font-size: 14px;
}

.text-muted {
  color: #909399;
}

@media (max-width: 768px) {
  .category-strategy-container.full-width-container {
    padding-top: 10px !important;
  }
}
</style>
