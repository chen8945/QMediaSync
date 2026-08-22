package baidupan

import "fmt"

// 错误码	描述	排查方向
// -1	权益已过期	权益已过期
// -3	文件不存在	文件不存在
// -6	身份验证失败	1.access_token 是否有效;
// 2.授权是否成功；
// 3. 参考接入授权 FAQ；
// 4.阅读文档《使用入门->接入授权》章节。
// -7	文件或目录名错误或无权访问	文件或目录名有误
// -8	文件或目录已存在	--
// -9	文件或目录不存在	--
// 0	请求成功	成功了
// 2	参数错误	1.检查必选参数是否都已填写；
// 2. 检查参数位置，有的参数是在 URL 里，有的是在 body 里；
// 3.检查每个参数的值是否正确
// 6	不允许接入用户数据	建议 10 分钟之后用户再进行授权重试。
// 10	转存文件已经存在	转存文件已经存在
// 11	用户不存在（uid 不存在）	--
// 12	批量转存出错	参数错误，检查转存源和目的是不是同一个 uid，正常不应该是一个 uid
// 111	有其他异步任务正在执行	稍后，可重新请求
// 133	播放广告	稍后，可重新请求
// 255	转存数量太多	转存数量太多
// 2131	该分享不存在	检查 tag 是否传的一个空文件夹
// 20011	应用审核中，仅限前 10 个完成 OAuth 授权的用户测试应用	完成应用上线审核，可放开授权用户数限制
// 20012	访问超限，调用次数已达上限，触发限流	1.检查是否完成应用上线审核，通过审核前，仅限用于测试开发
// 2.检查调用频率是否异常
// 20013	权限不足，当前应用无接口权限	1.检查是否完成应用上线审核，通过审核前，可能没有该接口权限
// 2.检查应用上线审核时是否申请该接口权限
// 3.重新发起上线审核，申请对应接口权限
// 31023	参数错误	1.检查必选参数是否都已填写；
// 2. 检查参数位置，有的参数是在 URL 里，有的是在 body 里；
// 3.检查每个参数的值是否正确
// 31024	没有访问权限	检查授权应用方式
// 31034	命中接口频控	接口请求过于频繁，注意控制
// 31045	access_token 验证未通过	请检查 access_token 是否过期，用户授权时是否勾选网盘权限等
// 31061	文件已存在	--
// 31062	文件名无效	检查是否包含特殊字符
// 31064	上传路径错误	上传文件的绝对路径格式：/apps/申请接入时填写的产品名称请参考《能力说明->限制条件->目录限制》
// 31066	文件名不存在	排查文件是否存储，路径是否传错
// 31190	文件不存在	1. block_list 参数是否正确；
// 2.一般是分片上传阶段有问题；
// 3.检查分片上传阶段，分片传完了么；
// 4. size 大小对不对，跟实际文件是否一致，跟预上传接口的 size 是否一致；
// 5.对照文档好好检查一下每一步相关的参数及值是否正确。
// 31299	第一个分片的大小小于 4 MB	要等于 4 MB
// 31301	非音视频文件	文件类型是否是音视频
// 31304	视频格式不支持播放	--
// 31326	命中防盗链	查看自己请求是否合理，User-Agent 请求头是否正常
// 31338	当前视频码率太高暂不支持流畅播放	用户下载后播放
// 31339	非法媒体文件	检查视频内容
// 31341	视频正在转码	可重新请求
// 31346	视频转码失败	排查该文件是否是个正常的视频
// 31347	当前视频太长，暂不支持在线播放	建议用户下载后播放
// 31355	参数异常	一般是 uploadid 参数传得有问题，确认 uploadid 参数是否与预上传 precreate 接口下发的 uploadid 一致
// 31360	URL 过期	请重新获取
// 31362	签名错误	请检查链接地址是否完整
// 31363	分片缺失	1.分片是否全部上传；每个上传的分片是否正确；
// 2. size 大小是否正确，跟实际文件是否一致，跟预上传接口的 size 是否一致；
// 3.对照文档好好检查一下每一步相关的参数及值是否正确
// 31364	超出分片大小限制	建议以 4 MB 作为上限
// 31365	文件总大小超限	授权用户为普通用户时，单个分片大小固定为 4 MB，单文件总大小上限为 4 GB
// 授权用户为普通会员时，单个分片大小上限为 16 MB，单文件总大小上限为 10 GB
// 授权用户为超级会员时，单个分片大小上限为 32 MB，单文件总大小上限为 20 GB
// 31649	字幕不存在	--
// 42202	文件个数超过相册容量上限	--
// 42203	相册不存在	--
// 42210	部分文件添加失败	--
// 42211	获取图片分辨率失败	--
// 42212	共享目录文件上传者信息查询失败	--
// 42213	共享目录鉴权失败	没有共享目录的权限
// 42214	获取文件详情失败	--
// 42905	查询用户名失败	可重试
// 50002	播单 ID 不存在	异常处理或者获取正确的播单 ID
var ErrorMap map[int64]string = map[int64]string{
	-1:    "权益已过期",
	-3:    "文件不存在",
	-6:    "身份验证失败",
	-7:    "文件或目录名错误或无权访问",
	-8:    "文件或目录已存在",
	-9:    "文件或目录不存在",
	0:     "请求成功",
	2:     "参数错误",
	6:     "不允许接入用户数据",
	10:    "转存文件已经存在",
	11:    "用户不存在（uid 不存在）",
	12:    "批量转存出错",
	111:   "有其他异步任务正在执行",
	133:   "播放广告",
	255:   "转存数量太多",
	2131:  "该分享不存在",
	20011: "应用审核中，仅限前 10 个完成 OAuth 授权的用户测试应用",
	20012: "访问超限，调用次数已达上限，触发限流",
	20013: "权限不足，当前应用无接口权限",
	31023: "参数错误",
	31024: "没有访问权限",
	31034: "命中接口频控",
	31045: "access_token 验证未通过",
	31061: "文件已存在",
	31062: "文件名无效",
	31064: "上传路径错误",
	31066: "文件名不存在",
	31190: "文件不存在",
	31299: "第一个分片的大小小于 4 MB",
	31301: "非音视频文件",
	31304: "视频格式不支持播放",
	31326: "命中防盗链",
	31338: "当前视频码率太高暂不支持流畅播放",
	31339: "非法媒体文件",
	31341: "视频正在转码",
	31346: "视频转码失败",
	31347: "当前视频太长，暂不支持在线播放",
	31355: "参数异常",
	31360: "URL 过期",
	31362: "签名错误",
	31363: "分片缺失",
	31364: "超出分片大小限制",
	31365: "文件总大小超限",
	31649: "字幕不存在",
	42202: "文件个数超过相册容量上限",
	42203: "相册不存在",
	42210: "部分文件添加失败",
	42211: "获取图片分辨率失败",
	42212: "共享目录文件上传者信息查询失败",
	42213: "共享目录鉴权失败",
	42214: "获取文件详情失败",
	42905: "查询用户名失败",
	50002: "播单 ID 不存在",
}

// 百度网盘 union 族接口（pan.baidu.com）中与访问凭证相关的 errno。
// OAuth 族接口（openapi.baidu.com）的 110/111 在本项目中仅经授权中转间接访问，
// 不经过 ErrorMap；111 在 union 族中始终是文件管理的异步任务语义，不参与凭证判断。
const (
	errnoTokenAuthFail   int64 = -6    // 身份验证失败
	errnoTokenExpired    int64 = 20016 // access_token 已过期
	errnoTokenInvalid    int64 = 20017 // access_token 无效（用户解绑或撤销授权）
	errnoTokenVerifyFail int64 = 31045 // access_token 验证未通过
)

// isTokenErrno 判断 union 族接口的 errno 是否表示访问凭证问题。
func isTokenErrno(errno int64) bool {
	switch errno {
	case errnoTokenAuthFail, errnoTokenExpired, errnoTokenInvalid, errnoTokenVerifyFail:
		return true
	}
	return false
}

// TokenInvalidError 表示百度网盘接口判定访问凭证无效或过期。
// 该错误不代表 refresh_token 失效，定时刷新成功后调用方即可恢复，无需重新授权。
type TokenInvalidError struct {
	Errno  int64
	Detail string
}

func (e *TokenInvalidError) Error() string {
	return fmt.Sprintf("百度网盘访问凭证无效或已过期（errno %d），等待定时刷新：%s", e.Errno, e.Detail)
}

// fs_id	uint64	文件在云端的唯一标识 ID
// path	string	文件的绝对路径
// server_filename	string	文件名称
// size	uint	文件大小，单位 B
// server_mtime	uint	文件在服务器修改时间
// server_ctime	uint	文件在服务器创建时间
// local_mtime	uint	文件在客户端修改时间
// local_ctime	uint	文件在客户端创建时间
// isdir	uint	是否为目录，0 文件、1 目录
// category	uint	文件类型，1 视频、2 音频、3 图片、4 文档、5 应用、6 其他、7 种子
// md5	string	云端哈希（非文件真实 MD5），只有是文件类型时，该字段才存在
// dir_empty	int	该目录是否存在子目录，只有请求参数 web=1 且该条目为目录时，该字段才存在，0 为存在，1 为不存在
// thumbs	array	只有请求参数 web=1 且该条目分类为图片时，该字段才存在，包含三个尺寸的缩略图 URL；不传 web 参数，则不返回缩略图地址
type FileInfo struct {
	FsId           uint64 `json:"fs_id"`
	Path           string `json:"path"`
	ServerFilename string `json:"server_filename"`
	Size           uint64 `json:"size"`
	ServerMtime    uint64 `json:"server_mtime"`
	ServerCtime    uint64 `json:"server_ctime"`
	LocalMtime     uint64 `json:"local_mtime"`
	LocalCtime     uint64 `json:"local_ctime"`
	IsDir          uint32 `json:"isdir"`
	Category       uint32 `json:"category"`
	Md5            string `json:"md5"`
	DirEmpty       uint32 `json:"dir_empty"`
}

type FileListResponse struct {
	Errno     int32       `json:"errno"`
	GuidInfo  string      `json:"guid_info"`
	List      []*FileInfo `json:"list"`
	RequestId int64       `json:"request_id"`
	Guid      int64       `json:"guid"`
}

// 参数名称	类型	描述
// list	json array	文件信息列表
// names	json	如果查询共享目录，该字段为共享目录文件上传者的 uk 和账户名称
// list[0] ["category"]	int	文件类型，含义如下：1 视频、2 音乐、3 图片、4 文档、5 应用、6 其他、7 种子
// list[0] ["dlink”]	string	文件下载地址，参考下载文档进行下载操作。注意 unicode 解码处理。
// list[0] ["filename”]	string	文件名
// list[0] ["isdir”]	int	是否是目录，1 表示目录，0 表示非目录
// list[0] ["server_ctime”]	int	文件的服务器创建 Unix 时间戳，单位秒
// list[0] ["server_mtime”]	int	文件的服务器修改 Unix 时间戳，单位秒
// list[0] ["size”]	int	文件大小，单位字节
// list[0] ["thumbs”]	object	缩略图地址，包含四种分辨率。详细尺寸参考响应示例
// list[0] ["height”]	int	图片高度
// list[0] ["width”]	int	图片宽度
// list[0] ["date_taken”]	int	图片拍摄时间
// list[0] ["orientation”]	string	图片旋转方向信息
// list[0] ["media_info”]	object	视频信息。
type FileDetail struct {
	Category    uint32 `json:"category"`
	Dlink       string `json:"dlink"`
	FileName    string `json:"filename"`
	IsDir       uint32 `json:"isdir"`
	ServerCtime uint64 `json:"server_ctime"`
	ServerMtime uint64 `json:"server_mtime"`
	Size        uint64 `json:"size"`
}

type FileDetailResponse struct {
	List []*FileDetail `json:"list"`
}

//	{
//	    "cursor": 5,
//	    "errmsg": "succ",
//	    "errno": 0,
//	    "has_more": 1,
//	    "list": [
//	        {
//	            "category": 4,
//	            "fs_id": 30995775581277,
//	            "isdir": 0,
//	            "local_ctime": 1547097900,
//	            "local_mtime": 1547097900,
//	            "md5": "540b49455n2f04f55c3929eb8b0c0445",
//	            "path": "/测试目录/20160820.txt",
//	            "server_ctime": 1578310087,
//	            "server_filename": "20160820.txt",
//	            "server_mtime": 1596078161,
//	            "size": 1120,
//	            "thumbs": {
//	                "url1": "https://thumbnail0.baidupcs.com/thumbnail/540b49455n2f04f55c3929eb8b0c0445?fid=2082810368-250528-30995775581277&rt=pr&sign=FDTAER-DCb740ccc5511e5e8fedcff06b081203-VDkr1qdq%2BXPk79PIL5SHCADhEnk%3D&expires=8h&chkbd=0&chkv=0&dp-logid=4193316540243014694&dp-callid=0&time=1596092400&size=c140_u90&quality=100",
//	                "url2": "https://thumbnail0.baidupcs.com/thumbnail/540b49455n2f04f55c3929eb8b0c0445?fid=2082810368-250528-30995775581277&rt=pr&sign=FDTAER-DCb740ccc5511e5e8fedcff06b081203-VDkr1qdq%2BXPk79PIL5SHCADhEnk%3D&expires=8h&chkbd=0&chkv=0&dp-logid=4193316540243014694&dp-callid=0&time=1596092400&size=c360_u270&quality=100",
//	                "url3": "https://thumbnail0.baidupcs.com/thumbnail/540b49455n2f04f55c3929eb8b0c0445?fid=2082810368-250528-30995775581277&rt=pr&sign=FDTAER-DCb740ccc5511e5e8fedcff06b081203-VDkr1qdq%2BXPk79PIL5SHCADhEnk%3D&expires=8h&chkbd=0&chkv=0&dp-logid=4193316540243014694&dp-callid=0&time=1596092400&size=c850_u580&quality=100"
//	            }
//	        },
//		}
//	}
type FileListAllItem struct {
	Category       uint32 `json:"category"`
	FsId           uint64 `json:"fs_id"`
	IsDir          uint32 `json:"isdir"`
	LocalCtime     uint64 `json:"local_ctime"`
	LocalMtime     uint64 `json:"local_mtime"`
	Md5            string `json:"md5"`
	Path           string `json:"path"`
	ServerCtime    uint64 `json:"server_ctime"`
	ServerFilename string `json:"server_filename"`
	ServerMtime    uint64 `json:"server_mtime"`
	Size           uint64 `json:"size"`
}

type FileListAllResponse struct {
	Cursor  uint32             `json:"cursor"`
	HasMore uint32             `json:"has_more"`
	List    []*FileListAllItem `json:"list"`
}
