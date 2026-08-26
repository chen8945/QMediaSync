# QMediaSync

QMediaSync 是一个媒体同步和刮削系统，用于管理 115 网盘、百度网盘、OpenList 等云存储与 Emby 媒体服务器之间的文件同步、STRM 生成和媒体刮削等流程。

## Docker 镜像

`ghcr.io/chen8945/qmediasync:latest`

从原项目迁移时，只需要将 Docker 镜像地址更换为以上地址。

## 部署说明

新部署可以参考原项目的 [Docker 安装说明](https://github.com/qicfan/qmediasync/wiki/Docker安装)，但初始化流程与原项目略有不同：原项目使用默认管理员账号和密码，本项目不提供默认管理员账号和密码，需要通过启动日志中的一次性初始化码自行创建首个管理员。

首次部署并启动后查看启动日志，找到“检测到系统尚未创建管理员，请使用以下初始化码完成首次管理员创建：”后面的初始化码。打开 QMediaSync Web 页面，登录页会显示“创建管理员”表单；填写初始化码、管理员用户名、密码和确认密码后提交即可。创建成功后，使用新建的管理员账号登录。

初始化码只在本次启动期间有效，创建首个管理员后立即失效。如果创建管理员前重启服务，请重新查看新一轮启动日志并使用新的初始化码；已有管理员时不会再生成初始化码。

## 原项目地址

本仓库基于以下原项目合并而来：

- 后端：[qicfan/qmediasync](https://github.com/qicfan/qmediasync)
- 前端：[qicfan/q115-strm-frontend](https://github.com/qicfan/q115-strm-frontend)
- Wiki：[qicfan/qmediasync/wiki](https://github.com/qicfan/qmediasync/wiki)
