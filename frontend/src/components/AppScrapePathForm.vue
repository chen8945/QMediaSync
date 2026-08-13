<template>
  <div class="scrape-path-form-page" :class="{ 'is-mobile': checkIsMobile }">
    <PageHeader actions-position="start">
      <template #actions>
        <el-button type="primary" :icon="ArrowLeft" @click="goBack" size="large" link>
          返回刮削目录
        </el-button>
      </template>
    </PageHeader>

    <template v-if="checkIsMobile">
      <el-form
        ref="formRef"
        :model="form"
        :rules="formRules"
        label-width="140px"
        label-position="top"
      >
        <el-form-item label="来源类型" prop="source_type" v-if="!isEditMode">
          <el-radio-group
            v-model="form.source_type"
            placeholder="请选择来源类型"
            @change="handleSourceTypeChange"
          >
            <el-radio-button
              v-for="typeItem in sourceTypeOptions"
              :key="typeItem.value"
              :value="typeItem.value"
            >
              {{ typeItem.label }}
            </el-radio-button>
          </el-radio-group>
          <div class="form-tip">
            <div v-if="form.source_type === 'local'">本地目录路径</div>
            <div v-if="form.source_type === '115'">需要先添加用于刮削的 115 账号并授权</div>
          </div>
        </el-form-item>
        <el-form-item
          label="网盘账号"
          prop="account_id"
          v-if="form.source_type !== 'local' && !isEditMode"
        >
          <el-select
            v-model="form.account_id"
            placeholder="请选择网盘账号"
            :loading="accountsLoading"
            :disabled="loading"
          >
            <template v-for="account in accounts">
              <el-option
                v-if="account.source_type === form.source_type && account.authorized"
                :key="account.id"
                :label="account.name"
                :value="account.id"
              ></el-option>
            </template>
          </el-select>
          <div class="form-tip">选择用于刮削的网盘账号</div>
        </el-form-item>
        <el-form-item label="媒体类型" prop="media_type" :disabled="loading || isEditMode">
          <el-radio-group v-model="form.media_type" placeholder="请选择媒体类型">
            <el-radio-button value="movie">电影</el-radio-button>
            <el-radio-button value="tvshow">电视剧</el-radio-button>
            <el-radio-button value="other">其他</el-radio-button>
          </el-radio-group>
          <div class="form-tip">其他：只能整理不能刮削</div>
        </el-form-item>
        <el-form-item label="操作方式" prop="scrape_type">
          <el-radio-group v-model="form.scrape_type">
            <el-radio-button value="only_scrape" :disabled="form.media_type === 'other'"
              >仅刮削</el-radio-button
            >
            <el-radio-button value="scrape_and_rename" :disabled="form.media_type === 'other'"
              >刮削和整理</el-radio-button
            >
            <el-radio-button value="only_rename">仅整理</el-radio-button>
          </el-radio-group>
          <div class="form-tip">
            仅刮削：不移动、不重命名文件，只生成对应视频的 NFO、封面等元数据，不需要选择目标路径<br />
            刮削和整理：根据刮削结果移动并重命名文件，同时生成对应视频的
            NFO、封面等元数据，需要选择目标路径<br />
            仅整理：不重新刮削，只使用已查询到的信息整理文件；媒体类型为“其他”时必须已有
            NFO，否则无法获取整理信息
          </div>
        </el-form-item>
        <el-form-item label="整理方式" prop="rename_type" v-if="form.scrape_type !== 'only_scrape'">
          <el-radio-group v-model="form.rename_type">
            <el-radio-button value="move">移动</el-radio-button>
            <el-radio-button value="copy">复制</el-radio-button>
            <el-radio-button value="soft_symlink" :disabled="form.source_type !== 'local'"
              >软链接</el-radio-button
            >
            <el-radio-button value="hard_symlink" :disabled="form.source_type !== 'local'"
              >硬链接</el-radio-button
            >
          </el-radio-group>
          <div class="form-tip">
            移动：将视频文件移动到目标路径，元数据（NFO、字幕等）也会生成或移动到目标路径<br />
            复制：将文件复制到目标路径，元数据（NFO、字幕等）也会生成或复制到目标路径<br />
            软链接：在目标路径创建文件软链接，元数据（NFO、字幕等）会生成或复制到目标路径<br />
            硬链接：在目标路径创建文件硬链接，元数据（NFO、字幕等）会生成或复制到目标路径
          </div>
        </el-form-item>
        <el-form-item
          label="来源路径"
          prop="source_path"
          v-if="
            (form.source_type !== 'local' && form.account_id) ||
            form.source_type === 'local' ||
            isEditMode
          "
        >
          <div class="pan-dir-input">
            <el-input
              v-model="form.source_path"
              placeholder="点击选择按钮选择目录"
              :disabled="loading"
              readonly
            />
            <el-button type="primary" @click="openDirSelector(true)" :disabled="loading">
              选择目录
            </el-button>
          </div>
          <div v-if="form.source_path != ''" class="selected-path-inline">
            <span class="path-label">选中目录路径：</span>
            <code class="path-url">{{ form.source_path }}</code>
          </div>
          <div class="form-tip">选择要刮削的源目录，系统会扫描该目录下的所有视频文件</div>
        </el-form-item>
        <el-form-item
          label="目标路径"
          prop="dest_path"
          v-if="
            ((form.source_type !== 'local' && form.account_id) ||
              form.source_type === 'local' ||
              isEditMode) &&
            form.scrape_type !== 'only_scrape'
          "
        >
          <div class="pan-dir-input">
            <el-input
              v-model="form.dest_path"
              placeholder="点击选择按钮选择目标目录"
              :disabled="loading"
              readonly
            />
            <el-button type="primary" @click="openDirSelector(false)" :disabled="loading">
              选择目录
            </el-button>
          </div>
          <div v-if="form.dest_path != ''" class="selected-path-inline">
            <span class="path-label">选中目录路径：</span>
            <code class="path-url">{{ form.dest_path }}</code>
          </div>
          <div class="form-tip">选择刮削后文件的存放位置</div>
        </el-form-item>
        <el-form-item
          label="开启二级分类"
          prop="enable_category"
          v-if="form.scrape_type !== 'only_scrape'"
        >
          <el-switch
            v-model="form.enable_category"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">
            开启后会按二级分类策略组织文件，并在目标路径下先创建二级分类目录
          </div>
        </el-form-item>
        <el-form-item
          label="文件夹重命名模板"
          prop="folder_name_template"
          v-if="form.scrape_type !== 'only_scrape'"
        >
          <el-input
            v-model="form.folder_name_template"
            :disabled="loading"
            placeholder="留空保留原名称"
          />
          <div class="form-tip">
            详细请参考：<a
              href="https://github.com/qicfan/qmediasync/wiki/%E6%95%B4%E7%90%86%E6%96%87%E4%BB%B6%EF%BC%88%E5%A4%B9%EF%BC%89%E6%A8%A1%E6%9D%BF%E5%8F%AF%E7%94%A8%E5%8F%98%E9%87%8F"
              target="_blank"
              rel="noopener noreferrer"
              >文件夹重命名模板</a
            >
          </div>
        </el-form-item>
        <el-form-item
          label="文件重命名模板"
          prop="file_name_template"
          v-if="form.scrape_type !== 'only_scrape'"
        >
          <el-input
            v-model="form.file_name_template"
            :disabled="loading"
            placeholder="留空保留原名称"
          />
          <div class="form-tip">
            详细请参考：<a
              href="https://github.com/qicfan/qmediasync/wiki/%E6%95%B4%E7%90%86%E6%96%87%E4%BB%B6%EF%BC%88%E5%A4%B9%EF%BC%89%E6%A8%A1%E6%9D%BF%E5%8F%AF%E7%94%A8%E5%8F%98%E9%87%8F"
              target="_blank"
              rel="noopener noreferrer"
              >文件重命名模板</a
            >
          </div>
        </el-form-item>
        <el-form-item label="要删除的关键词" prop="delete_keyword">
          <MetadataExtInput
            v-model="form.delete_keyword"
            placeholder="输入关键词后按回车添加"
            class="meta-ext-input limited-width-input"
            :autoAddDot="false"
          />
          <div class="form-tip">
            从视频文件名中提取影视剧标题前会先删除这些关键词，有助于提升识别准确率
          </div>
        </el-form-item>
        <el-form-item label="最小视频文件大小" prop="min_video_file_size">
          <el-input-number
            v-model="form.min_video_file_size"
            :min="0"
            :step="1"
            style="width: 100%"
            placeholder="请输入最小视频文件大小"
            :disabled="loading"
          ></el-input-number>
          <div class="form-tip">单位：MB，小于此值的视频文件将被忽略</div>
        </el-form-item>
        <el-form-item label="视频文件扩展名" prop="video_ext_list">
          <MetadataExtInput
            v-model="form.video_ext_list"
            placeholder="输入视频文件扩展名后按回车添加"
            class="meta-ext-input limited-width-input"
          />
          <div class="form-tip">支持的视频文件扩展名，用于筛选视频文件</div>
        </el-form-item>
        <el-form-item label="过滤无头像演员" prop="exclude_no_image_actor">
          <el-switch
            v-model="form.exclude_no_image_actor"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">没有头像的演员不会加入到 NFO 文件中</div>
        </el-form-item>
        <el-form-item label="删除整理完的非空路径" prop="force_delete_source_path">
          <el-switch
            v-model="form.force_delete_source_path"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">
            开启后，整理完成会强制删除源文件所在目录（常用于清理残留广告文件）；关闭时只会删除空目录
          </div>
        </el-form-item>
        <el-form-item label="刮削线程数" prop="max_threads">
          <el-input-number
            v-model="form.max_threads"
            :disabled="loading"
            :min="SCRAPE_THREAD_LIMITS.min"
            :max="
              form.source_type === 'local'
                ? SCRAPE_THREAD_LIMITS.localMax
                : SCRAPE_THREAD_LIMITS.remoteMax
            "
            :step="1"
            style="width: 100%"
          />
          <div class="form-help">
            本地文件刮削的最大并发数，值越高速度越快；网盘刮削不受该设置影响。默认
            5，只有配置了自己的 TMDB API Key 后才能修改本地目录并发数。
          </div>
        </el-form-item>
        <el-form-item label="启用 AI 识别" prop="enable_ai">
          <el-radio-group
            v-model="form.enable_ai"
            placeholder="请选择 AI 识别模式"
            :disabled="loading"
            size="large"
          >
            <el-radio-button value="off">禁用</el-radio-button>
            <el-radio-button value="assist">辅助识别</el-radio-button>
            <el-radio-button value="enforce">强制使用</el-radio-button>
          </el-radio-group>
          <div class="form-help">
            辅助识别：仅在其他方式无法识别时使用 AI。默认额度每天限 1000 次，长期使用建议配置自己的
            API Key。<br />
            强制使用：始终使用 AI 识别，必须配置自己的 API Key。
          </div>
        </el-form-item>
        <el-form-item label="提示词" prop="ai_prompt">
          <el-input
            v-model="form.ai_prompt"
            type="textarea"
            placeholder="请输入 AI 提示词"
            :disabled="loading || form.enable_ai === 'off'"
            :rows="4"
            maxlength="1000"
          />
          <div class="form-help">
            用于指导 AI 进行媒体识别的提示词，不确定如何设置时可以留空。<br />
            <span v-if="form.ai_prompt == ''">
              默认提示词：{{ defaultAiPrompt }}{{ form.ai_prompt }}{{ defaultAiPrompSuffix }}
            </span>
          </div>
        </el-form-item>
        <el-form-item label="定时任务" prop="enable_cron">
          <el-switch
            v-model="form.enable_cron"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">开启后会按 Cron 表达式定时执行刮削任务</div>
        </el-form-item>
        <template v-if="form.enable_cron">
          <el-form-item label="Cron 表达式" prop="cron_expression">
            <el-input
              v-model="form.cron_expression"
              placeholder="0 3 * * *"
              :disabled="loading"
              @change="validateCronExpression"
            />
            <div class="form-tip">
              标准 Cron 表达式格式：分 时 日 月 周（例如：0 3 * * * 表示每天凌晨 3 点）
            </div>
          </el-form-item>
          <el-form-item label="Cron 描述">
            <div class="cron-description">
              {{ form.cron_description || '请输入有效的 Cron 表达式' }}
            </div>
          </el-form-item>
          <el-form-item label="快速选择">
            <el-select
              v-model="selectedCronPreset"
              placeholder="选择预设模板"
              @change="applyCronPreset"
              :disabled="loading"
            >
              <el-option label="每天凌晨 3 点" value="0 3 * * *" />
              <el-option label="每天凌晨 2 点" value="0 2 * * *" />
              <el-option label="每 2 小时" value="0 */2 * * *" />
              <el-option label="每 6 小时" value="0 */6 * * *" />
              <el-option label="周一到周五凌晨 3 点" value="0 3 * * 1-5" />
              <el-option label="每周日凌晨 3 点" value="0 3 * * 0" />
              <el-option label="每月 1 号凌晨 3 点" value="0 3 1 * *" />
            </el-select>
          </el-form-item>
        </template>
        <el-form-item
          label="启用 fanart.tv"
          prop="enable_fanart_tv"
          v-if="form.media_type == 'movie'"
        >
          <el-switch
            v-model="form.enable_fanart_tv"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">
            开启后会从 fanart.tv 下载高清图片，速度较慢，可能降低刮削效率。
          </div>
        </el-form-item>
      </el-form>

      <div class="mobile-form-footer">
        <el-button @click="goBack">取消</el-button>
        <el-button type="primary" @click="handleSubmit" :loading="loading">
          {{ isEditMode ? '保存修改' : '确定添加' }}
        </el-button>
      </div>
    </template>

    <el-card v-else class="form-card">
      <el-form
        ref="formRef"
        :model="form"
        :rules="formRules"
        label-width="140px"
        label-position="left"
      >
        <el-form-item label="来源类型" prop="source_type" v-if="!isEditMode">
          <el-radio-group
            v-model="form.source_type"
            placeholder="请选择来源类型"
            @change="handleSourceTypeChange"
          >
            <el-radio-button
              v-for="typeItem in sourceTypeOptions"
              :key="typeItem.value"
              :value="typeItem.value"
            >
              {{ typeItem.label }}
            </el-radio-button>
          </el-radio-group>
          <div class="form-tip">
            <div v-if="form.source_type === 'local'">本地目录路径</div>
            <div v-if="form.source_type === '115'">需要先添加用于刮削的 115 账号并授权</div>
          </div>
        </el-form-item>
        <el-form-item
          label="网盘账号"
          prop="account_id"
          v-if="form.source_type !== 'local' && !isEditMode"
        >
          <el-select
            v-model="form.account_id"
            placeholder="请选择网盘账号"
            :loading="accountsLoading"
            :disabled="loading"
          >
            <template v-for="account in accounts">
              <el-option
                v-if="account.source_type === form.source_type && account.authorized"
                :key="account.id"
                :label="account.name"
                :value="account.id"
              ></el-option>
            </template>
          </el-select>
          <div class="form-tip">选择用于刮削的网盘账号</div>
        </el-form-item>
        <el-form-item label="媒体类型" prop="media_type">
          <el-radio-group
            v-model="form.media_type"
            placeholder="请选择媒体类型"
            :disabled="loading || isEditMode"
          >
            <el-radio-button value="movie">电影</el-radio-button>
            <el-radio-button value="tvshow">电视剧</el-radio-button>
            <el-radio-button value="other">其他</el-radio-button>
          </el-radio-group>
          <div class="form-tip">其他：只能整理不能刮削</div>
        </el-form-item>
        <el-form-item label="操作方式" prop="scrape_type">
          <el-radio-group v-model="form.scrape_type">
            <el-radio-button value="only_scrape" :disabled="form.media_type === 'other'"
              >仅刮削</el-radio-button
            >
            <el-radio-button value="scrape_and_rename" :disabled="form.media_type === 'other'"
              >刮削和整理</el-radio-button
            >
            <el-radio-button value="only_rename">仅整理</el-radio-button>
          </el-radio-group>
          <div class="form-tip">
            仅刮削：不移动、不重命名文件，只生成对应视频的 NFO、封面等元数据，不需要选择目标路径<br />
            刮削和整理：根据刮削结果移动并重命名文件，同时生成对应视频的
            NFO、封面等元数据，需要选择目标路径<br />
            仅整理：不重新刮削，只使用已查询到的信息整理文件；媒体类型为“其他”时必须已有
            NFO，否则无法获取整理信息
          </div>
        </el-form-item>
        <el-form-item label="整理方式" prop="rename_type" v-if="form.scrape_type !== 'only_scrape'">
          <el-radio-group v-model="form.rename_type">
            <el-radio-button value="move">移动</el-radio-button>
            <el-radio-button value="copy">复制</el-radio-button>
            <el-radio-button value="soft_symlink" :disabled="form.source_type !== 'local'"
              >软链接</el-radio-button
            >
            <el-radio-button value="hard_symlink" :disabled="form.source_type !== 'local'"
              >硬链接</el-radio-button
            >
          </el-radio-group>
          <div class="form-tip">
            移动：将视频文件移动到目标路径，元数据（NFO、字幕等）也会生成或移动到目标路径<br />
            复制：将文件复制到目标路径，元数据（NFO、字幕等）也会生成或复制到目标路径<br />
            软链接：在目标路径创建文件软链接，元数据（NFO、字幕等）会生成或复制到目标路径<br />
            硬链接：在目标路径创建文件硬链接，元数据（NFO、字幕等）会生成或复制到目标路径
          </div>
        </el-form-item>
        <el-form-item
          label="来源路径"
          prop="source_path"
          v-if="
            (form.source_type !== 'local' && form.account_id) ||
            form.source_type === 'local' ||
            isEditMode
          "
        >
          <div class="pan-dir-input">
            <el-input
              v-model="form.source_path_id"
              placeholder="点击选择按钮选择目录"
              :disabled="loading"
              readonly
            />
            <el-button type="primary" @click="openDirSelector(true)" :disabled="loading">
              选择目录
            </el-button>
          </div>
          <div v-if="form.source_path != ''" class="selected-path-inline">
            <span class="path-label">选中目录路径：</span>
            <code class="path-url">{{ form.source_path }}</code>
          </div>
          <div class="form-tip">选择要刮削的源目录，系统会扫描该目录下的所有视频文件</div>
        </el-form-item>
        <el-form-item
          label="目标路径"
          prop="dest_path"
          v-if="
            ((form.source_type !== 'local' && form.account_id) ||
              form.source_type === 'local' ||
              isEditMode) &&
            form.scrape_type !== 'only_scrape'
          "
        >
          <div class="pan-dir-input">
            <el-input
              v-model="form.dest_path_id"
              placeholder="点击选择按钮选择目标目录"
              :disabled="loading"
              readonly
            />
            <el-button type="primary" @click="openDirSelector(false)" :disabled="loading">
              选择目录
            </el-button>
          </div>
          <div v-if="form.dest_path != ''" class="selected-path-inline">
            <span class="path-label">选中目录路径：</span>
            <code class="path-url">{{ form.dest_path }}</code>
          </div>
          <div class="form-tip">选择刮削后文件的存放位置</div>
        </el-form-item>
        <el-form-item
          label="开启二级分类"
          prop="enable_category"
          v-if="form.scrape_type !== 'only_scrape'"
        >
          <el-switch
            v-model="form.enable_category"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">
            开启后会按二级分类策略组织文件，并在目标路径下先创建二级分类目录
          </div>
        </el-form-item>
        <el-form-item
          label="文件夹重命名模板"
          prop="folder_name_template"
          v-if="form.scrape_type !== 'only_scrape'"
        >
          <el-input
            v-model="form.folder_name_template"
            :disabled="loading"
            placeholder="留空保留原名称"
          />
          <div class="form-tip">
            详细请参考：<a
              href="https://github.com/qicfan/qmediasync/wiki/%E6%95%B4%E7%90%86%E6%96%87%E4%BB%B6%EF%BC%88%E5%A4%B9%EF%BC%89%E6%A8%A1%E6%9D%BF%E5%8F%AF%E7%94%A8%E5%8F%98%E9%87%8F"
              target="_blank"
              rel="noopener noreferrer"
              >文件夹重命名模板</a
            >
          </div>
        </el-form-item>
        <el-form-item
          label="文件重命名模板"
          prop="file_name_template"
          v-if="form.scrape_type !== 'only_scrape'"
        >
          <el-input
            v-model="form.file_name_template"
            :disabled="loading"
            placeholder="留空保留原名称"
          />
          <div class="form-tip">
            详细请参考：<a
              href="https://github.com/qicfan/qmediasync/wiki/%E6%95%B4%E7%90%86%E6%96%87%E4%BB%B6%EF%BC%88%E5%A4%B9%EF%BC%89%E6%A8%A1%E6%9D%BF%E5%8F%AF%E7%94%A8%E5%8F%98%E9%87%8F"
              target="_blank"
              rel="noopener noreferrer"
              >文件重命名模板</a
            >
          </div>
        </el-form-item>
        <el-form-item label="要删除的关键词" prop="delete_keyword">
          <MetadataExtInput
            v-model="form.delete_keyword"
            placeholder="输入关键词后按回车添加"
            class="meta-ext-input limited-width-input"
            :autoAddDot="false"
          />
          <div class="form-tip">
            从视频文件名中提取影视剧标题前会先删除这些关键词，有助于提升识别准确率
          </div>
        </el-form-item>
        <el-form-item label="最小视频文件大小" prop="min_video_file_size">
          <el-input-number
            v-model="form.min_video_file_size"
            :min="0"
            :step="1"
            style="width: 100%"
            placeholder="请输入最小视频文件大小"
            :disabled="loading"
          ></el-input-number>
          <div class="form-tip">单位：MB，小于此值的视频文件将被忽略</div>
        </el-form-item>
        <el-form-item label="视频文件扩展名" prop="video_ext_list">
          <MetadataExtInput
            v-model="form.video_ext_list"
            placeholder="输入视频文件扩展名后按回车添加"
            class="meta-ext-input limited-width-input"
          />
          <div class="form-tip">支持的视频文件扩展名，用于筛选视频文件</div>
        </el-form-item>
        <el-form-item label="过滤无头像演员" prop="exclude_no_image_actor">
          <el-switch
            v-model="form.exclude_no_image_actor"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">没有头像的演员不会加入到 NFO 文件中</div>
        </el-form-item>
        <el-form-item label="删除整理完的非空路径" prop="force_delete_source_path">
          <el-switch
            v-model="form.force_delete_source_path"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">
            开启后，整理完成会强制删除源文件所在目录（常用于清理残留广告文件）；关闭时只会删除空目录
          </div>
        </el-form-item>
        <el-form-item label="刮削线程数" prop="max_threads">
          <el-input-number
            v-model="form.max_threads"
            :disabled="loading"
            :min="SCRAPE_THREAD_LIMITS.min"
            :max="
              form.source_type === 'local'
                ? SCRAPE_THREAD_LIMITS.localMax
                : SCRAPE_THREAD_LIMITS.remoteMax
            "
            :step="1"
            style="width: 100%"
          />
          <div class="form-help">
            本地文件刮削的最大并发数，值越高速度越快；网盘刮削不受该设置影响。默认
            5，只有配置了自己的 TMDB API Key 后才能修改本地目录并发数。
          </div>
        </el-form-item>
        <el-form-item label="启用 AI 识别" prop="enable_ai">
          <el-radio-group
            v-model="form.enable_ai"
            placeholder="请选择 AI 识别模式"
            :disabled="loading"
            size="large"
          >
            <el-radio-button value="off">禁用</el-radio-button>
            <el-radio-button value="assist">辅助识别</el-radio-button>
            <el-radio-button value="enforce">强制使用</el-radio-button>
          </el-radio-group>
          <div class="form-help">
            辅助识别：仅在其他方式无法识别时使用 AI。默认额度每天限 1000 次，长期使用建议配置自己的
            API Key。<br />
            强制使用：始终使用 AI 识别，必须配置自己的 API Key。
          </div>
        </el-form-item>
        <el-form-item label="提示词" prop="ai_prompt">
          <el-input
            v-model="form.ai_prompt"
            type="textarea"
            placeholder="请输入 AI 提示词"
            :disabled="loading || form.enable_ai === 'off'"
            :rows="4"
            maxlength="1000"
          />
          <div class="form-help">
            用于指导 AI 进行媒体识别的提示词，不确定如何设置时可以留空。<br />
            <span v-if="form.ai_prompt == ''">
              默认提示词：{{ defaultAiPrompt }}{{ form.ai_prompt }}{{ defaultAiPrompSuffix }}
            </span>
          </div>
        </el-form-item>
        <el-form-item label="定时任务" prop="enable_cron">
          <el-switch
            v-model="form.enable_cron"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">开启后会按 Cron 表达式定时执行刮削任务</div>
        </el-form-item>
        <template v-if="form.enable_cron">
          <el-form-item label="Cron 表达式" prop="cron_expression">
            <el-input
              v-model="form.cron_expression"
              placeholder="0 3 * * *"
              :disabled="loading"
              @change="validateCronExpression"
            />
            <div class="form-tip">
              标准 Cron 表达式格式：分 时 日 月 周（例如：0 3 * * * 表示每天凌晨 3 点）
            </div>
          </el-form-item>
          <el-form-item label="Cron 描述">
            <div class="cron-description">
              {{ form.cron_description || '请输入有效的 Cron 表达式' }}
            </div>
          </el-form-item>
          <el-form-item label="快速选择">
            <el-select
              v-model="selectedCronPreset"
              placeholder="选择预设模板"
              @change="applyCronPreset"
              :disabled="loading"
            >
              <el-option label="每天凌晨 3 点" value="0 3 * * *" />
              <el-option label="每天凌晨 2 点" value="0 2 * * *" />
              <el-option label="每 2 小时" value="0 */2 * * *" />
              <el-option label="每 6 小时" value="0 */6 * * *" />
              <el-option label="周一到周五凌晨 3 点" value="0 3 * * 1-5" />
              <el-option label="每周日凌晨 3 点" value="0 3 * * 0" />
              <el-option label="每月 1 号凌晨 3 点" value="0 3 1 * *" />
            </el-select>
          </el-form-item>
        </template>
        <el-form-item
          label="启用 fanart.tv"
          prop="enable_fanart_tv"
          v-if="form.media_type == 'movie'"
        >
          <el-switch
            v-model="form.enable_fanart_tv"
            :active-value="true"
            :inactive-value="false"
            :disabled="loading"
          />
          <div class="form-tip">
            开启后会从 fanart.tv 下载高清图片，速度较慢，可能降低刮削效率。
          </div>
        </el-form-item>
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
      :title="isSelectSource ? '选择来源目录' : '选择目标目录'"
      :width="checkIsMobile ? '90%' : '600px'"
      :close-on-click-modal="false"
      body-class="directory-selector"
    >
      <div class="dir-selector">
        <DirectorySelector
          v-model="tempSelectedDir"
          :source-type="selectedSourceType"
          :account-id="selectedAccountId"
          @cancel="showDirDialog = false"
          @select="confirmSelectDir"
        />
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { SERVER_URL } from '@/const'
import PageHeader from '@/components/common/PageHeader.vue'
import { SCRAPE_THREAD_LIMITS } from '@/constants/validation'
import { useHttpClient } from '@/http/client'
import { onMounted, ref, reactive, watch, useTemplateRef } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { ArrowLeft } from '@element-plus/icons-vue'
import { useDeviceType } from '@/composables/useDeviceType'
import { navigateBackOrReplace } from '@/utils/navigation'
import { sourceTypeOptions } from '@/utils/sourceTypeUtils'
import MetadataExtInput from './MetadataExtInput.vue'
import DirectorySelector from './DirectorySelector.vue'
import type { DirInfo } from '@/typing'

interface CloudAccount {
  id: number
  name: string
  source_type: string
  user_id: string
  username: string
  created_at: number
  authorized: boolean
}

interface ScrapePath {
  id?: number
  source_type: string
  account_id?: number
  media_type: string
  source_path: string
  source_path_id: string
  dest_path: string
  dest_path_id: string
  scrape_type: string
  rename_type: string
  enable_category: boolean
  folder_name_template: string
  file_name_template: string
  delete_keyword: string[]
  min_video_file_size: number
  video_ext_list: string[]
  created_at?: number
  updated_at?: number
  enable_ai: string
  ai_prompt: string
  exclude_no_image_actor: boolean
  force_delete_source_path: boolean
  enable_cron?: boolean
  cron_expression?: string
  cron_description?: string
  enable_fanart_tv: boolean
  max_threads: number
}

const http = useHttpClient()
const route = useRoute()
const router = useRouter()

const defaultAiPrompt = [
  '请从文件名中提取电影名称和年份；名称中不要包含点、下划线、横杠、斜杠等特殊字符；如果文件名中包含 TMDB ID（格式 {tmdbid-123455}），请优先用它辅助识别。',
  '',
].join('\n')
const defaultAiPrompSuffix = [
  '',
  '输出格式：请严格按以下 JSON 格式返回，不要添加任何其他内容：{"name": "提取出的影视剧名称", "year": 年份或 0}',
  '现在请处理文件名：{{filename}}',
].join('\n')

const { isMobile: checkIsMobile } = useDeviceType()
const isEditMode = ref(false)
const loading = ref(false)

const formRef = useTemplateRef<FormInstance>('formRef')
const form = reactive<ScrapePath>({
  id: 0,
  source_type: '115',
  account_id: 0,
  media_type: 'movie',
  source_path: '',
  source_path_id: '',
  dest_path: '',
  dest_path_id: '',
  scrape_type: 'scrape_and_rename',
  rename_type: 'move',
  enable_category: false,
  folder_name_template: '{title} ({year})',
  file_name_template: '{title} ({year})',
  delete_keyword: [] as string[],
  min_video_file_size: 0,
  video_ext_list: [
    '.mp4',
    '.mkv',
    '.avi',
    '.mov',
    '.wmv',
    '.webm',
    '.flv',
    '.ts',
    '.m4v',
    '.iso',
    '.rmvb',
    '.strm',
  ],
  exclude_no_image_actor: false,
  enable_ai: 'off',
  ai_prompt: '',
  force_delete_source_path: false,
  enable_cron: false,
  cron_expression: '',
  cron_description: '',
  enable_fanart_tv: false,
  max_threads: SCRAPE_THREAD_LIMITS.remoteMax,
})

const formRules: FormRules = {
  source_type: [{ required: true, message: '请选择来源类型', trigger: 'change' }],
  account_id: [{ required: true, message: '请选择网盘账号', trigger: 'change' }],
  media_type: [{ required: true, message: '请选择媒体类型', trigger: 'change' }],
  source_path: [
    { required: true, message: '请选择来源目录', trigger: 'blur' },
    { min: 1, max: 500, message: '长度在 1 到 500 个字符', trigger: 'blur' },
  ],
  source_path_id: [{ required: true, message: '请选择来源目录 ID', trigger: 'blur' }],
  dest_path_id: [{ required: true, message: '请选择目标目录 ID', trigger: 'blur' }],
  scrape_type: [{ required: true, message: '请选择操作方式', trigger: 'change' }],
  rename_type: [
    {
      required: form.scrape_type !== 'only_scrape',
      message: '请选择整理方式',
      trigger: 'change',
    },
  ],
  min_video_file_size: [
    { type: 'number', min: 0, message: '最小视频文件大小必须大于等于 0', trigger: 'change' },
  ],
  video_ext_list: [
    { type: 'array', required: true, message: '请至少添加一个视频文件扩展名', trigger: 'change' },
  ],
}

const accounts = ref<CloudAccount[]>([])
const accountsLoading = ref(false)

const selectedCronPreset = ref('')

const showDirDialog = ref(false)
const tempSelectedDir = ref<DirInfo | null>(null)
const isSelectSource = ref(true)
const selectedSourceType = ref('')
const selectedAccountId = ref(0)

const goBack = () => {
  void navigateBackOrReplace(router, { name: 'scrape-pathes' })
}

const returnToScrapePathList = () => {
  void router.replace({ name: 'scrape-pathes' })
}

const handleSourceTypeChange = () => {
  if (form.source_type !== 'local') {
    loadAccounts()
  }
}

const loadAccounts = async () => {
  accounts.value = []
  try {
    accountsLoading.value = true
    const response = await http.get(`${SERVER_URL}/account/list`, {
      params: { source_type: form.source_type },
    })
    if (response?.data.code === 200) {
      accounts.value = response.data.data || []
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

const loadDirectoryData = async (id: number) => {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/scrape/pathes/${id}`)

    if (response?.data.code === 200) {
      // const directory = response.data.data?.find((d: ScrapePath) => d.id === id)
      // if (directory) {
      // 将 res.data.data 赋值给 form
      const directory = response.data.data
      form.id = directory.id || 0
      form.source_type = directory.source_type
      form.account_id = directory.account_id
      form.media_type = directory.media_type
      form.source_path = directory.source_path
      form.source_path_id = directory.source_path_id
      form.dest_path = directory.dest_path
      form.dest_path_id = directory.dest_path_id
      form.scrape_type = directory.scrape_type
      form.rename_type = directory.rename_type
      form.enable_category = directory.enable_category
      form.folder_name_template = directory.folder_name_template
      form.file_name_template = directory.file_name_template
      form.delete_keyword = [...directory.delete_keyword]
      form.min_video_file_size = directory.min_video_file_size || 0
      form.video_ext_list = directory.video_ext_list || []
      form.exclude_no_image_actor = directory.exclude_no_image_actor || false
      form.enable_ai = directory.enable_ai || 'off'
      form.ai_prompt = directory.ai_prompt || ''
      form.force_delete_source_path = directory.force_delete_source_path || false
      form.enable_cron = directory.enable_cron || false
      form.cron_expression = directory.cron_expression || ''
      form.cron_description = directory.cron_description || ''
      form.enable_fanart_tv = directory.enable_fanart_tv || false
      form.max_threads = parseInt(directory.max_threads + '') || SCRAPE_THREAD_LIMITS.remoteMax

      // } else {
      //   ElMessage.error('未找到该刮削目录')
      //   goBack()
      // }
    } else {
      ElMessage.error(response?.data.message || '加载刮削目录失败')
      goBack()
    }
  } catch {
    console.error('加载刮削目录错误')
    ElMessage.error('加载刮削目录失败')
    goBack()
  } finally {
    loading.value = false
  }
}

const openDirSelector = (isSource: boolean = false) => {
  showDirDialog.value = true
  tempSelectedDir.value = null
  selectedSourceType.value = form.source_type
  selectedAccountId.value = Number(form.account_id) || 0
  isSelectSource.value = isSource
}

const confirmSelectDir = () => {
  if (!tempSelectedDir.value) return
  if (isSelectSource.value) {
    form.source_path = tempSelectedDir.value.path
    form.source_path_id = tempSelectedDir.value.id
  } else {
    form.dest_path = tempSelectedDir.value.path
    form.dest_path_id = tempSelectedDir.value.id
  }

  showDirDialog.value = false
}

// 验证并解析 Cron 表达式
const validateCronExpression = async () => {
  if (!form.cron_expression || form.cron_expression.trim() === '') {
    form.cron_description = ''
    return
  }

  try {
    const response = await http.post(`${SERVER_URL}/cron/validate`, {
      cron_expression: form.cron_expression.trim(),
    })

    if (response?.data.code === 200) {
      form.cron_description = response.data.data.description || '有效表达式'
    } else {
      ElMessage.warning(response?.data.message || 'Cron 表达式验证失败')
      form.cron_description = '无效表达式'
    }
  } catch (error) {
    console.error('验证 Cron 表达式失败：', error)
    ElMessage.error('验证 Cron 表达式失败，请检查网络连接')
  }
}

// 应用预设的 Cron 表达式（快速选择）
const applyCronPreset = () => {
  if (!selectedCronPreset.value) {
    return
  }

  form.cron_expression = selectedCronPreset.value
  selectedCronPreset.value = ''
  validateCronExpression()
}

const handleSubmit = async () => {
  if (!formRef.value) return
  if (form.scrape_type !== 'only_scrape' && form.dest_path_id === '') {
    ElMessage.error('请先选择目标路径，并确认重命名模板已填写')
    return
  }

  try {
    await formRef.value.validate()
    loading.value = true

    if (isEditMode.value) {
      const response = await http.post(`${SERVER_URL}/scrape/pathes`, {
        id: form.id,
        source_path: form.source_path,
        source_path_id: form.source_path_id,
        dest_path: form.dest_path,
        dest_path_id: form.dest_path_id,
        scrape_type: form.scrape_type,
        rename_type: form.rename_type,
        enable_category: form.enable_category,
        folder_name_template: form.folder_name_template,
        file_name_template: form.file_name_template,
        delete_keyword: form.delete_keyword,
        min_video_file_size: form.min_video_file_size,
        video_ext_list: form.video_ext_list,
        exclude_no_image_actor: form.exclude_no_image_actor,
        enable_ai: form.enable_ai,
        ai_prompt: form.ai_prompt,
        force_delete_source_path: form.force_delete_source_path,
        enable_cron: form.enable_cron,
        cron_expression: form.cron_expression || '',
        cron_description: form.cron_description || '',
        enable_fanart_tv: form.enable_fanart_tv,
        max_threads: parseInt(form.max_threads + ''),
      })

      if (response?.data.code === 200) {
        ElMessage.success('编辑刮削目录成功')
        returnToScrapePathList()
      } else {
        ElMessage.error(response?.data.message || '编辑刮削目录失败')
      }
    } else {
      const response = await http.post(`${SERVER_URL}/scrape/pathes`, {
        id: 0,
        source_type: form.source_type,
        account_id: form.source_type !== 'local' ? form.account_id : 0,
        media_type: form.media_type,
        source_path: form.source_path,
        source_path_id: form.source_path_id,
        dest_path: form.dest_path,
        dest_path_id: form.dest_path_id,
        scrape_type: form.scrape_type,
        rename_type: form.rename_type,
        enable_category: form.enable_category,
        folder_name_template: form.folder_name_template,
        file_name_template: form.file_name_template,
        delete_keyword: form.delete_keyword,
        min_video_file_size: form.min_video_file_size,
        video_ext_list: form.video_ext_list,
        exclude_no_image_actor: form.exclude_no_image_actor,
        enable_ai: form.enable_ai,
        ai_prompt: form.ai_prompt,
        force_delete_source_path: form.force_delete_source_path,
        enable_cron: form.enable_cron,
        cron_expression: form.cron_expression || '',
        cron_description: form.cron_description || '',
        enable_fanart_tv: form.enable_fanart_tv,
        max_threads: form.max_threads,
      })

      if (response?.data.code === 200) {
        ElMessage.success('添加刮削目录成功')
        returnToScrapePathList()
      } else {
        ElMessage.error(response?.data.message || '添加刮削目录失败')
      }
    }
  } catch {
    console.error('提交刮削目录错误')
    ElMessage.error(isEditMode.value ? '编辑刮削目录失败' : '添加刮削目录失败')
  } finally {
    loading.value = false
  }
}

watch(
  () => form.media_type,
  (newType) => {
    if (newType === 'other') {
      form.scrape_type = 'only_rename'
      form.folder_name_template = '{actors}/{num}'
      form.file_name_template = '{num}'
    } else if (
      newType === 'movie' &&
      (form.file_name_template.includes('{num}') ||
        form.file_name_template.includes('{season_episode}'))
    ) {
      form.folder_name_template = '{title} ({year})'
      form.file_name_template = '{title} ({year})'
    } else if (newType === 'tvshow' && !form.file_name_template.includes('{season_episode}')) {
      form.folder_name_template = '{title} ({year})'
      form.file_name_template = '{title} - {season_episode} - 第 {episode_number} 集'
    }
  },
)

watch(
  () => form.scrape_type,
  (newType) => {
    if (newType === 'only_scrape') {
      form.rename_type = 'same'
      form.enable_category = false
    }
  },
)

onMounted(async () => {
  const id = route.params.id as string
  if (id) {
    isEditMode.value = true
    await loadDirectoryData(Number(id))
  } else {
    await loadAccounts()
  }
})
</script>

<style scoped>
.scrape-path-form-page {
  padding: 20px;
}

.form-card {
  width: 100%;
  border-radius: 8px;
  box-shadow: 0 2px 12px 0 rgba(0, 0, 0, 0.1);
  margin-top: 16px;
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

.form-help {
  font-size: 12px;
  color: #606266;
  margin-top: 8px;
  line-height: 1.6;
}

.form-help p {
  margin: 4px 0;
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
  height: 500px;
}

.scrape-path-form-page.is-mobile {
  padding: 12px;
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

.meta-ext-input {
  width: 100%;
}
</style>
