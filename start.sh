#!/bin/bash
echo "正在启动 学管会一体化综合管理系统..."
cd "$(dirname "$0")/backend"
go build -o xgh_server ./cmd/server
./xgh_server
