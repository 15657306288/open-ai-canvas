package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"infinite-canvas/backend/internal/database"
	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// 只迁移 1.57 豆包域所需的三张业务表，不触碰云端其它表。
// 账号池在 1.57 中是全局池（doubao_accounts 没有 user_id），因此归属核对以“全局池”报告，
// 不会把账号错误归到某个用户；如果未来需要按用户隔离，应先新增独立 owner 字段和迁移。
func main() {
	sourcePath := strings.TrimSpace(os.Getenv("SQLITE_SOURCE_PATH"))
	targetDSN := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if sourcePath == "" || targetDSN == "" {
		log.Fatal("必须配置 SQLITE_SOURCE_PATH 和 DATABASE_URL")
	}
	source, err := database.Open(database.Config{Driver: "sqlite", DSN: "file:" + sourcePath + "?mode=ro&_busy_timeout=5000"})
	if err != nil {
		log.Fatalf("连接 SQLite 失败：%v", err)
	}
	target, err := database.Open(database.Config{Driver: "postgres", DSN: targetDSN})
	if err != nil {
		log.Fatalf("连接 PostgreSQL 失败：%v", err)
	}
	source = source.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	target = target.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})

	var accounts []model.DoubaoAccount
	var metas []model.DoubaoPoolMeta
	var proxies []model.NetworkProxy
	for _, query := range []struct {
		name string
		dst  any
	}{
		{"doubao_accounts", &accounts},
		{"doubao_pool_meta", &metas},
		{"network_proxies", &proxies},
	} {
		if err := source.Table(query.name).Find(query.dst).Error; err != nil {
			log.Fatalf("读取 SQLite %s 失败：%v", query.name, err)
		}
	}
	if err := target.Transaction(func(tx *gorm.DB) error {
		if err := database.MigrateSchema(tx); err != nil {
			return fmt.Errorf("目标 schema 迁移：%w", err)
		}
		if err := upsertRows(tx, accounts); err != nil {
			return fmt.Errorf("导入 doubao_accounts：%w", err)
		}
		if err := upsertRows(tx, metas); err != nil {
			return fmt.Errorf("导入 doubao_pool_meta：%w", err)
		}
		if err := upsertRows(tx, proxies); err != nil {
			return fmt.Errorf("导入 network_proxies：%w", err)
		}
		var missing int64
		if err := tx.Model(&model.DoubaoAccount{}).Where("proxy_id <> '' AND proxy_id NOT IN (SELECT id FROM network_proxies)").Count(&missing).Error; err != nil {
			return err
		}
		if missing > 0 {
			return fmt.Errorf("发现 %d 个账号绑定了不存在的代理，已拒绝提交", missing)
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	// 归属核对：模型源表没有 user_id，账号属于全局池；打印用户数仅用于人工确认，不写入归属。
	var users int64
	_ = target.Model(&model.User{}).Count(&users).Error
	log.Printf("豆包数据导入完成：账号 %d，池元数据 %d，代理 %d；账号池归属=全局（云端用户数=%d，未强行绑定用户）", len(accounts), len(metas), len(proxies), users)
}

func upsertRows[T any](tx *gorm.DB, rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(&rows, 100).Error; err != nil {
		return err
	}
	return nil
}
