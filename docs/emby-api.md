# Go-Emby 接口文档：现有 Emby 兼容能力与项目扩展

> 核对日期：2026-11-12。实现基线：开发主线 `18ef2a4ca08d611e561731c794f168aac18513db`。
> 本文由当前路由、处理函数、DTO 和项目永久说明整理，描述已存在的接口，不是新增功能设计，也不承诺完整实现 Emby/Jellyfin API。公共仓库里的历史源码不代表本次核对的实现版本；本文发布不包含任何源码同步。实际部署版本较旧时，可能缺少下列能力。

## 1. 范围与约定

Go-Emby 是面向 Emby 客户端的媒体服务器。主要流程是登录、浏览媒体库、获取播放信息、直接播放和回报进度。默认 HTTP 端口为 `8097`，对接时应使用自己部署的服务地址。

- API 主要使用 HTTP + JSON；图片、字幕、本地视频返回相应资源类型。
- 下文 Emby 路由同时支持根路径和 `/emby` 前缀，例如 `/Items` 和 `/emby/Items`。文件管理 `/api/files*` 在独立入口匹配，不套用这一约定。
- Emby 主分派路由及公共查询读取通常不区分大小写；媒体 ID、游标、签名和 URL 应原样保存。不要依赖任意扩展路径或未知参数被处理。
- 表格中的方法是对接应使用的方法。部分旧路由没有严格拒绝其他方法，这不代表额外的方法语义得到支持。
- 时间长度和位置使用 Emby ticks：`1 秒 = 10,000,000 ticks`。日期字段按具体接口使用 UTC/RFC3339 等格式。
- 大多数 API 默认 `Cache-Control: no-store`；图片按资源路径提供私有缓存/条件请求。
- `OPTIONS` 返回 `204`。CORS 支持不替代身份校验和媒体库权限。

### 1.1 支持边界

| 能力 | 当前行为 |
| --- | --- |
| 电影、剧、季、集、媒体库浏览 | 已有实现，受媒体库权限过滤 |
| STRM / 远程 HTTP(S) 播放 | 普通入口返回 302，客户端继续访问源地址 |
| 本地真实视频 | 授权后原样 HTTP 直传，支持 GET、HEAD、Range、206 |
| 播放信息、音轨、外挂字幕 | 使用已有媒体信息及可用字幕；字段随资源完整度变化 |
| 服务端转码、转封装、HLS | 不提供；不能靠传入转码参数启用 |
| 媒体探测 | Go 原生解析与有界 HTTP Range；不依赖外部 ffprobe/ffmpeg |
| 下载媒体、同步下载 | 现有下载/同步入口禁用；本地播放 Range 不等于开放下载 API |
| 完整 Emby/Jellyfin 功能集 | 不作承诺，未列出的协议不能据此推定支持 |

## 2. 身份认证与权限

### 2.1 凭据形式

常用请求头：

```http
X-Emby-Token: <ACCESS_TOKEN>
X-Emby-Authorization: MediaBrowser Client="ExamplePlayer", Device="ExampleDevice", DeviceId="example-device", Version="1.0"
```

也支持以下已有凭据入口：

- `X-MediaBrowser-Token` 请求头。
- `api_key` 查询参数；兼容的 `X-Emby-Token`、`X-MediaBrowser-Token` 查询参数。
- `X-Emby-Authorization` 或 `Authorization` 中的 `Token="..."`，以及兼容的 `X-Emby-Authorization` 查询形式。
- `Authorization: Bearer <ACCESS_TOKEN>`。
- `X-Emby-Api-Key` 请求头或查询参数。

存在多个凭据时按服务端既有优先顺序读取，并非逐个尝试到成功。建议每次只提供一个有效凭据。URL 中的 Token、签名直链均应视作敏感信息，避免贴入公开日志。

`DeviceId` 来自授权字段、`DeviceId` / `X-Emby-Device-Id` 查询参数或对应设备请求头。登录时未提供会生成设备 ID；客户端应稳定发送同一个设备身份。

### 2.2 用户令牌与 API Key

| 身份 | 适用范围与限制 |
| --- | --- |
| 用户登录令牌 | 当前用户浏览、播放、进度、收藏；管理员账号另有管理能力 |
| 管理员登录令牌 | 管理后台及受保护的用户管理；首位管理员等安全保护仍生效 |
| API Key | 已有工具查询和部分标准管理接口；没有用户身份，不能调用 `/Users/Me` 或写入个人播放状态；`/admin/*` 与文件管理要求管理员账号 |

不要将 `api_key` 参数名理解成“只接受 API Key”：该参数也可承载用户登录令牌。工具身份也不是普通用户客户端的替代品；播放器应先登录。

媒体访问通过统一权限范围处理：普通用户不能任意切换到别人的 `UserId`；管理员/API Key 对支持的浏览入口指定用户时，使用目标用户的可见库和播放状态。无用户覆盖时继承权限组，无组或无新规则时保留旧行为，用户明确的库规则优先于组规则，隐藏库限制继续有效。详情、图片、字幕和实际播放同样检查权限，知道媒体 ID 不代表有权访问。

权限改变后后续请求按当前规则处理，旧登录令牌不绕过新权限。已经交给客户端的第三方直链及已建立的外部连接，撤销依赖第三方源本身的能力。

## 3. 服务信息与登录

| 方法 | 路径 | 身份 | 返回与说明 |
| --- | --- | --- | --- |
| GET | `/health` | 无需登录 | 数据库检查正常为 `{"status":"ok"}`；不可用为 503 |
| GET | `/System/Ping` | 无需登录 | 文本 `Emby Server` |
| GET | `/System/Info/Public` | 无需登录 | 服务器标识、名称、协议兼容版本、地址等 |
| GET | `/System/Info` | 已认证 | 服务器信息 |
| GET | `/System/Endpoint` | 已认证 | 当前请求的端点信息 |
| GET | `/Users/Public` | 无需登录 | 空数组，不公开账号列表 |
| POST | `/Users/AuthenticateByName` | 无需登录 | 登录并返回用户令牌 |
| GET | `/Users/Me` | 用户令牌 | 当前用户 DTO |
| GET | `/Users/{userId}` | 本人或有权管理者 | 用户 DTO |
| POST | `/Sessions/Logout` | 已认证 | 撤销当前令牌，204 |

登录使用 JSON，亦兼容 `application/x-www-form-urlencoded` 的 `Username`、`Pw` 字段：

```http
POST /emby/Users/AuthenticateByName
Content-Type: application/json
X-Emby-Authorization: MediaBrowser Client="ExamplePlayer", Device="ExampleDevice", DeviceId="example-device", Version="1.0"

{"Username":"example-user","Pw":"<PASSWORD>"}
```

成功响应主要字段为 `User`、`AccessToken`、`ServerId`、`SessionInfo`。`SessionInfo` 包含设备/客户端身份，声明不支持远程控制；不要把登录会话等同于正在播放。当前新登录令牌有效期为 30 天；首次密码设置要求仍可能限制后续 API。错误账号/密码返回 401，登录频率过高返回 429。

`System/Info` 的 `Version` 是 Emby 协议兼容值，不等于 Go-Emby 构建版本，更不能据此推定完整 Emby 能力。

## 4. 媒体库、列表与详情

### 4.1 路由索引

以下浏览入口要求认证，并受当前用户媒体库权限约束。

| 方法 | 路径 | 返回/用途 |
| --- | --- | --- |
| GET | `/Users/{userId}/Views` | 用户媒体库，分页对象形状 |
| GET | `/Library/MediaFolders` | 复用用户媒体库视图 |
| GET | `/Library/VirtualFolders` | 兼容虚拟文件夹数组，包含库及路径信息 |
| GET | `/Library/VirtualFolders/Query` | 虚拟文件夹查询，支持分页与总数 |
| GET | `/Library/SelectableMediaFolders` | 已授权媒体库及 `SubFolders`；投影已有根目录，不遍历磁盘 |
| GET | `/Users/{userId}/Items/Root` | 用户根目录 DTO |
| GET | `/Items`、`/Users/{userId}/Items` | 浏览、搜索、筛选与分页 |
| GET | `/Items/{itemId}/Children` | 复用 Items，以该 ID 作为 ParentId |
| GET | `/Items/{itemId}`、`/Users/{userId}/Items/{itemId}` | 单条详情 DTO |
| GET | `/Items/Latest`、`/Users/{userId}/Items/Latest` | 最近添加，返回数组 |
| GET | `/Items/Resume`、`/Users/{userId}/Items/Resume` | 继续播放，返回列表对象 |
| GET | `/Shows/{seriesId}/Seasons` | 季列表 |
| GET | `/Shows/{seriesId}/Episodes` | 单集列表；支持 SeasonId，按季集数字排序 |
| GET | `/Shows/NextUp` | 下一集推荐 |
| GET | `/Items/{itemId}/Similar` | 相似内容列表 |
| GET | `/Movies/Recommendations` | 按已有观看状态产生推荐分组，可能为空数组 |
| GET | `/Items/Counts` | MovieCount、EpisodeCount、SeriesCount |
| GET | `/Persons`、`/Persons/{personId或名称}` | 人物列表与详情 |
| GET/HEAD | `/Genres`、`/Genres/{名称或ID}` | 类型列表及详情 |
| GET/HEAD | `/Tags`、`/Tags/{名称或ID}` | 标签列表及详情 |
| GET/HEAD | `/Items/Filters`、`/Items/Filters2` | 筛选选项；Filters2 的 Genres 为 Name/Id 对象数组 |
| GET | `/DisplayPreferences/{id}` | 显示偏好；普通路径提供兼容默认值，部分虚拟分类有专门处理 |
| GET | `/Users/{userId}/GroupingOptions` | 兼容空数组 |

不存在独立 `/UserViews` 入口；客户端应使用上表实际实现的 Views 路径。类型/标签 ID 可通过普通 Item 详情入口读取，不要自行生成它们。

### 4.2 Items 查询参数

| 参数 | 当前用途 |
| --- | --- |
| `ParentId` | 库、目录、剧或季的范围；使用接口返回的 ID |
| `Recursive` | 是否递归，影响库和目录的展示层级 |
| `Ids` | 逗号分隔 ID 查询 |
| `SearchTerm`、`NameStartsWith` | 搜索或名称前缀 |
| `IncludeItemTypes` | 类型筛选，例如 Movie、Series、Season、Episode、Folder |
| `StartIndex`、`Limit` | 偏移与页长；普通 Items 默认 60、最大 200；剧/季下列表可到 10000 |
| `Cursor` | 服务端签名游标；应原样使用响应 NextCursor |
| `EnableTotalRecordCount` | false 时可避免精确总数计算；此时不要把 TotalRecordCount 当最终精确总数 |
| `SortBy`、`SortOrder` | 排序字段及 Ascending/Descending；支持逗号分隔组合 |
| `Fields` | 按需字段，例如 Path、MediaSources、MediaStreams、People、Etag；不等于开放完整 Emby Fields 集合 |
| `EnableUserData` | false 时列表可省去个人状态填充 |
| `Filters` | 已有 IsFavorite、IsPlayed、IsUnplayed、IsResumable 过滤 |
| `IsFavorite`、`IsPlayed`、`IsFolder` | 对应布尔筛选 |
| `Genres`、`GenreIds`、`Tags`、`TagIds` | 类型/标签名称或 ID，多值支持逗号或竖线 |
| `Person`、`PersonIds`、`Season`、`Years` | 人物、季编号、年份筛选 |

常用排序包括 `SortName`、`Name`、`ProductionYear`、`DateCreated`、`LatestAdded`、`DateLastContentAdded`、`PremiereDate`、`IndexNumber`、`ParentIndexNumber`、`Random`、`CommunityRating`、`CriticRating`、`DatePlayed`、`PlayCount`、`OfficialRating`、`SeriesDateLastContentAdded`、`SeriesDateLastReleased`。具体库、最近添加、继续播放和虚拟分类可能有自己的默认排序；不要假定所有列表都按名称排。

普通 Items 响应示意：

```json
{
  "Items": [],
  "TotalRecordCount": 0,
  "StartIndex": 0,
  "NextCursor": "",
  "HasMore": false
}
```

有下一页时可返回 `X-Next-Cursor`。游标绑定认证/查询/可见范围且会过期；查询改变、签名无效或过期时重新请求第一页。不要在更改筛选或切换账号后复用旧游标。Latest 返回数组，不要按上述对象解析。

### 4.3 DTO 注意事项

常用字段包括 `Id`、`ServerId`、`ParentId`、`Name`、`Type`、`IsFolder`、`ProductionYear`、`ImageTags`、`BackdropImageTags`、`UserData`；详情按资源补充 `Overview`、`ProviderIds`、`People`、`MediaSources`、`MediaStreams` 等。列表使用较轻的 DTO，不保证具有详情的全部字段。缺少音轨/时长等不能理解为允许服务端转码。

Episode 的单集名优先使用本地 NFO 中文 title、TMDB zh-CN 单集中文名、已有中文名，再回退非中文单集名；不会以电视剧总标题冒充单集标题。季集编号用于数字排序。

详情、请求 Path 的列表及工具源查询可能保留原始源路径/完整 STRM URL，属于敏感数据。播放时必须使用 PlaybackInfo 返回的播放入口。

## 5. 图片

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/HEAD | `/Items/{itemId}/Images/{type}` | 例如 Primary、Backdrop、Thumb、Logo、Banner、Disc |
| GET/HEAD | `/Items/{itemId}/Images/{type}/{index}` | 已有多图索引，例如 Backdrop/0；无效索引返回 404 |
| GET/HEAD | `/Persons/{personId或名称}/Images/{type}` | 人物图片 |
| GET/HEAD | `/Genres/{名称或ID}/Images/{type}` | 已有类型图入口 |
| GET/HEAD | `/Users/{userId}/Images/Primary` | 用户头像，兼容末尾 /0 |

图片支持已有 `tag`、尺寸和格式处理，例如 `maxWidth`、`maxHeight`；不能推定所有 Emby 编码参数都有效，`quality` 也不能视作任意编码质量控制。应以返回的 `ImageTags` / `BackdropImageTags` 和实际响应为准。

图片通常需要令牌。当前用户绑定的服务端签名 tag 可在对应图片请求中充当受限凭据，并再次检查当前权限；任意字符串 tag、旧裸 tag 或 User-Agent 都不能授予访问权。成功常见为 200，有效条件请求可为 304；没有图片为 404。上游图片失败、超时和正常请求取消是不同情况，不应统称播放故障。

## 6. PlaybackInfo 与直接播放

### 6.1 获取播放信息

```http
POST /emby/Items/<ITEM_ID>/PlaybackInfo
X-Emby-Token: <ACCESS_TOKEN>
Content-Type: application/json

{}
```

也支持 GET。响应主要结构：

```json
{
  "MediaSources": [
    {
      "Id": "<MEDIA_SOURCE_ID>",
      "Path": "<AUTHENTICATED_RELATIVE_PLAYBACK_URL>",
      "DirectStreamUrl": "<AUTHENTICATED_RELATIVE_PLAYBACK_URL>",
      "Protocol": "Http",
      "SupportsDirectPlay": true,
      "SupportsDirectStream": true,
      "SupportsTranscoding": false,
      "MediaStreams": []
    }
  ],
  "PlaySessionId": "<PLAY_SESSION_ID>"
}
```

这是结构示例，不是所有源的完整字段清单。普通用户会话的 `Path` 与受鉴权相对 `DirectStreamUrl` 一致，客户端应相对服务器地址解析。API Key/明确的后台工具源查询保留原始源路径语义，不能据此推断用户播放入口会泄露同样的地址。

PlaybackInfo 是协商与读取已有信息：不占用同时播放设备名额，不等待源站解析或媒体探测完成。明确选择 `MediaSourceId` 时可调度现有后台信息任务，但不阻塞返回。GET query 或 POST JSON 可传 `MediaSourceId`；当前仍返回版本源列表，不能把该参数理解为列表必然只剩一个源。实际播放入口校验并选择相应版本。

`DeviceProfile`、码率或转码开关等参数不能启用不存在的能力；也不承诺完整解析 Emby 播放协商参数。客户端应选择自己能解码的直接播放源。

### 6.2 取流入口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET/HEAD | `/Videos/{itemId}/stream`、`/Videos/{itemId}/stream.{ext}` | 标准直接播放入口 |
| GET/HEAD | `/Videos/{itemId}/original`、`/Videos/{itemId}/original.{ext}` | 同一播放链路的兼容入口 |
| GET/HEAD | `/Items/{itemId}/Playback/stream[.{ext}]` | 已有 Fast Path 兼容入口，应使用返回 URL |

使用服务端生成的完整相对 URL，保留其查询参数；常见参数有 `Static=true`、`api_key`、`MediaSourceId`。不要将后缀改为期待转码的格式，也不要自行拼接内部等待/续接参数。

实际取流先检查可见媒体、播放权限和用户设备限制；不属于当前条目的 `MediaSourceId` 返回 404。用户正常播放在此进入设备租约流程，进度心跳维持租约，停止事件释放相应播放状态。

| 源类型 | 成功行为 | 客户端要求 |
| --- | --- | --- |
| 本地真实文件 | 200；合法 Range 为 206，越界可能 416；HEAD 无正文 | 直接解码原文件，不依赖转码 |
| 普通远程 HTTP(S)/STRM | 302 + Location，源站的后续跳转由客户端处理 | 跟随跳转访问第三方视频 |
| 开启的已有 Fast Path | 仍为受鉴权播放入口，可使用已有加速/续接及回退逻辑 | 原样使用返回 URL，不把某个第三方当作必需依赖 |

本地直传检查路径边界，不提供任意文件读取；远程视频正文不由 Go-Emby 中继。不能把“无视频代理”误解为“本地文件不能播放”。Fast Path 是现有可选能力，其错误/回退受当前配置和源类型约束，不能承诺所有失败都无条件回退。PlaybackInfo 本身不主动请求第三方 resolver。

`/Videos/*` 下不支持的转码/HLS 路径返回 400。普通 stream 不提供转码；`Static` 也不是转码开关。除标准直接播放外，不声明通用视频资源接口均可用。

## 7. 字幕与音轨

音轨和字幕信息随 `MediaSources[].MediaStreams` 返回。外挂字幕可包含 `Type=Subtitle`、`Index`、`Language`、`Codec`、`IsExternal`、`DeliveryMethod=External`、`DeliveryUrl` 等字段。应根据返回的源 ID、索引和 URL 选择字幕，不要假定固定索引。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/HEAD | `/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/Stream.{ext}` | 标准外部字幕读取 |
| GET/HEAD | `/Videos/{itemId}/Subtitles/{cacheId}/Stream.{ext}` | 已有缓存字幕入口 |

已有扩展为 `srt`、`ass`、`ssa`、`vtt`。要求有效身份、可见媒体及相应播放权限；请求扩展必须匹配实际字幕，不能借扩展名请求格式转换。不存在、不匹配或不可见的字幕返回 404。

这些受保护的播放字幕入口与通用字幕下载不同：其他未命中有效播放字幕路由的 `/Subtitles` 请求及媒体下载入口仍禁用。Go-Emby 不通过转码切换音轨或烧录字幕，解码和轨道选择由播放器完成。

## 8. 播放事件、个人状态与收藏

### 8.1 播放生命周期

| 方法 | 路径 | 成功 |
| --- | --- | --- |
| POST | `/Sessions/Playing` | 204 |
| POST | `/Sessions/Playing/Progress` | 204 |
| POST | `/Sessions/Playing/Stopped` | 204 |
| POST | `/Sessions/Capabilities`、`/Sessions/Capabilities/Full` | 兼容接收，204；不代表远程控制能力 |

使用用户令牌，API Key 不能代替用户回报播放事件。示例：

```json
{
  "ItemId": "<ITEM_ID>",
  "MediaSourceId": "<MEDIA_SOURCE_ID>",
  "PlaySessionId": "<PLAY_SESSION_ID>",
  "PositionTicks": 1200000000,
  "RunTimeTicks": 54000000000,
  "IsPaused": false
}
```

始终携带正确的媒体/版本/会话 ID，按实际播放器时钟报告。`PositionTicks` 未提供与显式零不同；负位置表示不可用时钟，非停止事件不会因此维持播放在线。服务端已有旧会话与延迟停止保护；停止旧单集不应释放当前单集的设备占用。暂停、心跳和继续播放是不同状态，不能用电影总时长或简单开始/结束时间差冒充有效观看时长。

### 8.2 个人状态接口

下列写操作仅限当前登录用户，管理员也不能通过这些入口任意写入他人的个人状态；API Key 不适用。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET/POST | `/Users/{userId}/Items/{itemId}/UserData` | 读取/更新个人状态，返回 UserData |
| POST/DELETE | `/Users/{userId}/PlayedItems/{itemId}` | 标记已看/未看 |
| POST/DELETE | `/Users/{userId}/FavoriteItems/{itemId}` | 收藏/取消收藏 |
| POST | `/Users/{userId}/FavoriteItems/{itemId}/Delete` | 兼容取消收藏 |
| POST | `/Users/{userId}/Items/{itemId}/HideFromResume?Hide=true` | 隐藏或恢复继续播放展示，返回 UserData |
| DELETE | `/Items/{itemId}/Resume` | 清理该媒体的继续播放状态；剧集可能作用于关联整剧 |
| DELETE | `/Items/Resume` | 清理当前用户继续播放进度，204；保留收藏、已看标记及独立历史计数 |

UserData 更新支持 `PlaybackPositionTicks`、`Played`、`IsFavorite`、`PlayCount`、`LastPlayedDate`；负位置/非法计数被拒绝，`LastPlayedDate` 使用 RFC3339 日期。已看接口可选 `DatePlayed` 使用 `yyyyMMddHHmmss` 格式。

HideFromResume 优先读取有效的 `Hide=true/false` query；已有 Filmly 请求兼容分支可接收 JSON Hide，但通用客户端应使用标准 query。隐藏不等于清空进度，且剧/集隐藏可能覆盖所属剧的单集范围。批量清理有实际数据影响，客户端应明确展示作用范围。

## 9. 标准管理兼容接口

| 方法 | 路径 | 权限/限制 |
| --- | --- | --- |
| GET | `/Users`、`/Users/Query` | 管理员/API Key；Query 返回 Items、TotalRecordCount，当前未实现 StartIndex/Limit 分页 |
| POST | `/Users/New` | 管理员/API Key；Name、可选 Password，仍做密码校验 |
| POST | `/Users/{userId}/Policy` | 管理员/API Key，管理员保护继续生效 |
| GET/POST | `/Users/{userId}/Configuration` | 管理员/API Key 或当前普通用户的自身配置 |
| POST | `/Users/{userId}/Password` | 自身修改或授权管理者操作，具体字段/保护按身份处理 |
| DELETE | `/Users/{userId}` | 管理员/API Key，受账号保护规则约束 |
| POST | `/Users/{userId}/Delete` | 同上，兼容删除入口 |
| GET | `/Sessions` | 管理员/API Key 的只读会话查询，普通用户拒绝 |
| GET | `/ScheduledTasks` | 管理员/API Key；只读投影当前扫描器状态，不是完整 Emby 调度器 |
| POST | `/Library/Refresh` | 管理员/API Key；无范围时请求库刷新，有选择器时定向刷新 |
| POST | `/Items/{itemId}/Refresh` | 管理员/API Key；复用已有扫描队列 |
| DELETE | `/Items/{itemId}` | 现有媒体删除流程，受媒体删除权限和路径安全约束 |

`ScheduledTasks` 返回 `Key=RefreshLibrary` 的现有任务投影，状态为 Idle、Running 或 Cancelling；查询不启动扫描。不据此承诺 `/ScheduledTasks/Running/*` 等写入路由。

定向刷新支持 `ItemId` / `Id`、`Path` query，必须落在授权的有效扫描范围，非法、空或冲突选择器不会退化为全库扫描。成功接收返回 204，表示请求进入现有队列，不表示扫描/刮削已完成。对接时尽量提供明确范围，避免反复触发整库刷新。

## 10. Go-Emby 扩展入口索引

本节区分项目自身功能与 Emby 协议。以下为已存在的功能入口索引，不是完整后台请求体规范；写入字段、确认和任务控制应沿用对应版本 Web 管理界面，不要根据 URL 猜测请求体。

### 10.1 管理后台

`/admin/*` 要求管理员账号登录，普通用户和 API Key 不可替代。主要路由分组如下：

| 功能 | 已有路径 |
| --- | --- |
| 媒体库与目录 | `/admin/libraries`、`/admin/libraries/batch`、`/admin/library-folders`、`/admin/library-settings`、`/admin/library-visibility`、`/admin/media-boundaries`、`/admin/directories`、`/admin/order` |
| 用户与权限组 | `/admin/users`、`/admin/user-identity`、`/admin/permission-groups`、`/admin/user-group`、`/admin/user-libraries` |
| 设备 | `/admin/user-devices`、`/admin/device-login`；历史设备、活跃登录和正在播放分别处理 |
| API Key | `/admin/keys`；`/admin/fragments/keys` 返回 HTML fragment |
| 扫描 | `/admin/scan`、`/admin/scan-all`、`/admin/scan-control`、`/admin/scan-schedule` |
| 刮削与 TMDB | `/admin/scraper` 及其 plan、start、cancel、control、retry、tree 子路径，`/admin/tmdb` |
| 媒体信息 | `/admin/media-info`、`/admin/media-info/status`、`/admin/media-info/retry`、`/admin/media-info/restore`、`/admin/media-info/automation`，已有 batch 子路径 |
| 手工元数据 | `/admin/media-item`、`/admin/media-metadata` 及 directory、image-default、move-library 子路径 |
| 封面 | `/admin/cover`、`/admin/library-cover`、`/admin/library-cover-batch`、`/admin/library-cover-preview` |
| 字幕与片头片尾 | `/admin/subtitle`、`/admin/subtitle/cache`、`/admin/intro-credits`、`/admin/intro-credits/records` |
| 播放与增强设置 | `/admin/enhancements`、`/admin/fast-cache`、`/admin/proxy-settings`、`/admin/proxy-settings/test` |
| 任务与控制台 | `/admin/dashboard`、`/admin/console`、`/admin/dashboard/activity`、`/admin/dashboard/metrics`、`/admin/dashboard/tasks`、`/admin/tasks/events` |
| 状态、日志与诊断 | `/admin/status`、`/admin/logs`、`/admin/diagnostics/browse`、`/admin/diagnostics/versions` |
| 备份与迁移 | `/admin/backup`、`/admin/backups` 及其子路径、`/admin/emby-migration/*` |
| 已有外部服务集成 | `/admin/xiaoya`、`/admin/xiaoya/sync`、`/admin/xiaoya/remote-strm`、`/admin/telegram`、`/admin/telegram/test` |

`/admin/tasks/events` 是已有 SSE 任务事件入口，不能按普通 JSON 轮询解析。带 secret 的管理子接口处理敏感设置，不应在客户端日志公开其响应。

观看审计功能在本基线已移除，历史数据保留不代表当前存在可用的审计查询/清理 API。设备管理仍存在，不能将设备清理理解为删除账号、Token、收藏或观看进度。

### 10.2 文件管理

要求管理员账号，路径受文件管理根目录约束并拒绝越界/符号链接等非法访问。这组路径独立于 Emby 路由前缀，不能用媒体 Item ID 代替文件路径。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/files` | 目录浏览 |
| POST | `/api/files/rename` | 重命名 |
| POST | `/api/files/mkdir` | 建目录 |
| POST | `/api/files/move` | 移动 |
| POST | `/api/files/upload` | 上传 |
| GET/HEAD | `/api/files/download` | 管理员文件读取，与禁用的 Emby 媒体下载入口不同 |

### 10.3 其他已有入口

- `GET /Enhancements`：认证后的增强功能配置投影。
- `/Auth/Playback`：已有反向代理播放鉴权入口；部署对接应沿用既有鉴权约定，不是匿名视频代理。
- `/Trakt/Authorize`、`/Trakt/Poll`、`/Trakt/Sync`：已有 Trakt 集成入口，按用户身份和配置使用。
- `/System/Updates`、`/System/Updates/Status`、`/System/Updates/Settings`：已有更新管理入口；不属于客户端播放流程。

## 11. 错误与兼容边界

多数 JSON 错误为：

```json
{"error":"<MESSAGE>","Message":"<MESSAGE>"}
```

| 状态 | 常见含义 |
| --- | --- |
| 200 / 204 | 返回结果 / 操作成功且无正文 |
| 206 / 304 | 本地媒体 Range / 图片条件缓存命中 |
| 302 | 远程播放重定向；不是媒体正文响应 |
| 400 | 无效 JSON/参数/游标，或不支持的转码路径 |
| 401 | 缺少或失效身份；登录错误 |
| 403 | 账号/媒体播放/设备数/管理权限限制，或禁用下载 |
| 404 | 未实现接口、不可见/不存在媒体、源或图片/字幕缺失 |
| 405 | 对应处理器拒绝该 HTTP 方法 |
| 409 | 部分后台设置写入或任务冲突 |
| 416 | 本地文件 Range 无效 |
| 422 | 播放源格式/目标不符合现有入口要求 |
| 429 | 登录频率限制 |
| 500 / 503 | 操作失败 / 数据库或认证、播放校验服务暂不可用 |
| 502 / 504 | 相关图片或可选外部源处理失败/超时，需按接口区分 |

状态码取决于实际入口：例如独立文件管理鉴权失败返回 403，而主 Emby 认证入口通常为 401；不能强行用一个统一规则解释全部路由。

明确的限制：

- `CanDownload=false`；通用 `/Download`、未匹配播放字幕的 `/Subtitles`、`/Sync*` 路由禁用。
- 未发现专门的缺集服务实现，不把 `/Shows/Missing` 当作受支持的缺集查询；旧的宽匹配也不等于具备其协议语义。
- 不承诺 `AnyProviderIdEquals`、`MinDateLastSaved` 等未处理的 Items 查询语义。收到 200 不代表所有传入参数都生效。
- 不提供完整远程控制、转码、HLS 或完整 Jellyfin 功能集；兼容占位响应不等于功能实现。
- 当前接口存在不等于所有播放器版本都已现场验收。客户端反馈应注明实际构建版本、路径、方法和脱敏响应，不以 User-Agent 版本字符串作为授权依据。

## 12. 最小对接顺序与文档依据

1. 请求 `/System/Info/Public`，以自己配置的服务器地址作为后续请求基址。
2. 登录获取 `AccessToken`、用户 ID，稳定保存设备 ID。
3. 请求 `/Users/{userId}/Views` 获取库 ID，再通过 Items 浏览或搜索。
4. 获取单条详情；图片沿用返回的图片类型/tag，附带正确身份。
5. 调用 PlaybackInfo，保留 `PlaySessionId`、源 ID、完整相对播放 URL。
6. 播放该 URL；本地处理 Range，远程跟随 302。字幕使用实际返回的 DeliveryUrl。
7. 按实际状态回报 Playing、Progress、Stopped；使用个人状态接口处理收藏/已看。

本文通过静态核对整理，未声称执行真实客户端、生产播放或全量接口验收。公开示例只含占位符，不包含私库源码、生产地址、配置、密钥或真实签名 URL。

实现核对范围为当前基线的主路由与认证、浏览/分页/类型标签、播放/本地流、字幕、个人状态、用户管理、定向刷新、任务及文件管理处理器；同时核对项目永久说明及已有兼容/权限文档。旧审计中“缺少 SelectableMediaFolders”等历史结论已由当前代码更新，不沿用为现状。

写作结构参考 [MovieClaw 的 Jellyfin 兼容接口文档](https://github.com/movieclaw/MovieClaw/blob/main/docs/design/jellyfin-compat.md) 的协议整理方式；本文端点、字段和行为均以 Go-Emby 现有实现独立核对，不采用其功能设计或转码策略，也不因参考文档新增任何接口。
