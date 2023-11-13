package model

import (
	"time"
)

type EntryAttachment struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	EntryId   uint64 `gorm:"primaryKey;autoIncrement:false"`
	Url       string `gorm:"primaryKey;autoIncrement:false"`
}

// EntryAttachment
func AddEntryAttachment(newEntryAttachment EntryAttachment) error {
	// insert to db
	err := createEntryAttachment(database, newEntryAttachment)
	if err != nil {
		return err
	}
	return nil
}

func AddEntryAttachments(newEntryAttachments []EntryAttachment) error {
	// insert to db
	err := createEntryAttachments(database, newEntryAttachments)
	if err != nil {
		return err
	}
	return nil
}
