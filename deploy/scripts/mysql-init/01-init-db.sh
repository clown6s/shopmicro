#!/bin/bash
# ShopMicro MySQL 初始化脚本
# MySQL 8.4 首次启动时自动执行
# 注意: 此脚本必须以 .sh 结尾才能被 entrypoint 执行

set -e

# 确保字符集
mysql -u root -p"${MYSQL_ROOT_PASSWORD}" -e \
    "ALTER DATABASE IF EXISTS ${MYSQL_DATABASE} CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;" \
    2>/dev/null || mysql -u root -p"${MYSQL_ROOT_PASSWORD}" -e \
    "ALTER DATABASE ${MYSQL_DATABASE} CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"

# 授权远程访问 — 密码从环境变量读取
mysql -u root -p"${MYSQL_ROOT_PASSWORD}" -e "
CREATE USER IF NOT EXISTS 'shopmicro'@'%' IDENTIFIED BY '${MYSQL_ROOT_PASSWORD}';
GRANT ALL PRIVILEGES ON ${MYSQL_DATABASE}.* TO 'shopmicro'@'%';
FLUSH PRIVILEGES;
" 2>/dev/null

echo "MySQL init completed: user 'shopmicro'@'%' created."
