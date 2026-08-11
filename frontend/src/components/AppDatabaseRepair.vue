<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import PageHeader from '@/components/common/PageHeader.vue'

const http = useHttpClient()
const loading = ref(false)

const repairDatabase = async () => {
  try {
    loading.value = true
    const response = await http.post(`${SERVER_URL}/database/repair`)
    if (response?.data.code === 200) {
      ElMessage.success('数据库修复成功')
    } else {
      ElMessage.error(response?.data.message || '数据库修复失败')
    }
  } catch (error) {
    console.error('数据库修复失败：', error)
    ElMessage.error('数据库修复失败，请重试')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="main-content-container database-repair-container">
    <PageHeader />

    <div class="section-card">
      <div class="repair-content">
        <p class="repair-description">
          本操作会补齐缺失的数据表、字段和索引，不会删除已存在的表和数据；PostgreSQL
          会同步检查并修复主键序列。如果有以下问题：<br />
          日志错误提示：SQL logic error: no such table: 表名 <br />
          日志错误提示：pg duplicate key value violates unique constraint "表名_pkey" <br />
          <br />

          都可以执行修复数据库来解决问题。
        </p>
        <el-button type="warning" size="large" :loading="loading" @click="repairDatabase" round>
          {{ loading ? '修复中…' : '修复数据库' }}
        </el-button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.database-repair-container {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.database-repair-container :deep(.qms-page-header) {
  margin-bottom: 0;
}

.section-card {
  background: white;
  border-radius: 16px;
  padding: 20px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.08);
  border: 1px solid #dcdfe6;
}

.repair-content {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 20px;
  padding: 40px 20px;
}

.repair-description {
  font-size: 14px;
  color: #606266;
  text-align: left;
  margin: 0;
  line-height: 1.6;
}
</style>
