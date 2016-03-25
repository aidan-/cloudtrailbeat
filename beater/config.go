package beater

type CloudTrailConfig struct {
	SQSUrl                *string `config:"sqs_url"`
	AWSCredentialProvider *string `config:"aws_credential_provider"`
	AWSRegion             *string `config:"aws_region"`
	NoPurge               *bool   `config:"no_purge"`
	NumQueueFetch         *int    `config:"num_queue_fetch"`
	SleepTime             *int    `config:"sleep_time"`
}

type ConfigSettings struct {
	Input CloudTrailConfig
}
