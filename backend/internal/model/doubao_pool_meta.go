package model

import "time"

// DoubaoPoolMeta 豆包账号池的单例元数据（ID 恒为 "pool"，记录当前活跃账号）。
type DoubaoPoolMeta struct {
	ID        string    `gorm:"size:32;primaryKey" json:"id"`
	ActiveID  string    `gorm:"size:64" json:"activeId"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (DoubaoPoolMeta) TableName() string { return "doubao_pool_meta" }
