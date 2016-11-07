// Config is put into a different package to prevent cyclic imports in case
// it is needed in several locations

package config

import "time"

type Config struct {
	Period        time.Duration `config:"period"`
	SQSURL        string        `config:"sqs_url"`
	AWSRegion     string        `config:"aws_region"`
	NumQueueFetch int           `config:"num_queue_fetch"`
	NoPurge       bool          `config:"no_purge"`
}

var DefaultConfig = Config{
	Period:        300 * time.Second,
	AWSRegion:     "us-east-1",
	NumQueueFetch: 1,
	NoPurge:       true,
}
