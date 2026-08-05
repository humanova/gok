package model

import (
	"time"
)

type Request struct {
	ID         uint `gorm:"primarykey"`
	CreatedAt  time.Time
	Url        string
	StatusCode uint16
}

func AddRequests(newRequests []Request) error {
	// insert to db
	err := createRequests(database, newRequests)
	if err != nil {
		return err
	}
	return nil
}
