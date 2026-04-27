package models

import "time"

type Expense struct {
	ID          uint  `gorm:"primaryKey"`
	UserID      int64 `gorm:"index"`
	Amount      float64
	Description string
	CreatedAt   time.Time
}
