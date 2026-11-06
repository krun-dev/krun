package domain

import "time"

type WorkerAssetSession struct {
	ID, Organization, Worker, UploadHash, CompletionHash string
	Created, Expires                                     time.Time
	Files                                                []WorkerAssetSessionFile
}
type WorkerAssetSessionFile struct {
	Path, Hash, ObjectKey, ContentType string
	Size                               int
	Uploaded                           bool
}
