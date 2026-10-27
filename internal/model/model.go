package model

const (
	DefaultSegmentConcurrency = 4
	MaxSegmentConcurrency     = 16
)

type Settings struct {
	DownloadPath             string `json:"downloadPath,omitempty"`
	PollIntervalMinutes      int    `json:"pollIntervalMinutes"`
	ConcurrentDownloads      int    `json:"concurrentDownloads"`
	SegmentConcurrency       int    `json:"segmentConcurrency"`
	VerifyVideoAfterDownload bool   `json:"verifyVideoAfterDownload"`
	DownloadsPaused          bool   `json:"downloadsPaused"`
}

type StorageCategory struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ParentID     string `json:"parentId,omitempty"`
	DownloadPath string `json:"downloadPath"`
	CreatedAt    string `json:"createdAt"`
}

type Subscription struct {
	ID                string `json:"id"`
	StorageCategoryID string `json:"storageCategoryId"`
	Title             string `json:"title"`
	SourceURL         string `json:"sourceUrl"`
	Poster            string `json:"poster"`
	Year              string `json:"year"`
	Category          string `json:"category"`
	Region            string `json:"region"`
	Description       string `json:"description"`
	Status            string `json:"status"`
	AutoDownload      bool   `json:"autoDownload"`
	EpisodeCount      int    `json:"episodeCount"`
	DownloadedCount   int    `json:"downloadedCount"`
	AddedAt           string `json:"addedAt"`
	LastCheckedAt     string `json:"lastCheckedAt,omitempty"`
	LastError         string `json:"lastError,omitempty"`
}

type Episode struct {
	ID             string `json:"id"`
	SubscriptionID string `json:"subscriptionId"`
	Number         int    `json:"number"`
	Title          string `json:"title"`
	PageURL        string `json:"pageUrl"`
	Image          string `json:"image"`
	Description    string `json:"description"`
	PublishedAt    string `json:"publishedAt,omitempty"`
}

type DownloadTask struct {
	ID             string  `json:"id"`
	SubscriptionID string  `json:"subscriptionId"`
	EpisodeID      string  `json:"episodeId"`
	Title          string  `json:"title"`
	SeriesTitle    string  `json:"seriesTitle"`
	Poster         string  `json:"poster"`
	Status         string  `json:"status"`
	Progress       float64 `json:"progress"`
	Speed          string  `json:"speed,omitempty"`
	FilePath       string  `json:"filePath,omitempty"`
	Error          string  `json:"error,omitempty"`
	CreatedAt      string  `json:"createdAt"`
	StartedAt      string  `json:"startedAt,omitempty"`
	CompletedAt    string  `json:"completedAt,omitempty"`
}

type State struct {
	Settings      Settings          `json:"settings"`
	Categories    []StorageCategory `json:"categories"`
	Subscriptions []Subscription    `json:"subscriptions"`
	Episodes      []Episode         `json:"episodes"`
	Downloads     []DownloadTask    `json:"downloads"`
}

type SearchResult struct {
	Title        string `json:"title"`
	SourceURL    string `json:"sourceUrl"`
	Poster       string `json:"poster"`
	Description  string `json:"description"`
	Year         string `json:"year,omitempty"`
	Category     string `json:"category,omitempty"`
	Region       string `json:"region,omitempty"`
	EpisodeCount int    `json:"episodeCount,omitempty"`
}

type Series struct {
	SearchResult
	Episodes []Episode `json:"episodes"`
}
