package beater

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/sqs"

	"github.com/elastic/beats/libbeat/beat"
	"github.com/elastic/beats/libbeat/cfgfile"
	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/logp"
	"github.com/elastic/beats/libbeat/publisher"
)

const logTimeFormat = "2006-01-02T15:04:05Z"

// CloudTrailbeat contains configuration options specific to the current
//  running instance as defined in cmd line arguments and the configuration
//  file.
type CloudTrailbeat struct {
	sqsURL        string
	awsConfig     *aws.Config
	numQueueFetch int
	sleepTime     time.Duration
	noPurge       bool

	backfillBucket string
	backfillPrefix string

	CTbConfig   ConfigSettings
	CmdLineArgs CmdLineArgs
	events      publisher.Client
	done        chan struct{}
}

// CmdLineArgs is used by the flag package to parse custom flags specific
//  to CloudTrailbeat
type CmdLineArgs struct {
	backfillBucket *string
	backfillPrefix *string
}

var cmdLineArgs CmdLineArgs

// SQS message extracted from raw sqs event Body
type sqsMessage struct {
	Type             string
	MessageID        string
	TopicArn         string
	Message          string
	Timestamp        string
	SignatureVersion string
	Signature        string
	SigningCertURL   string
	UnsubscribeURL   string
}

// CloudTrail specific information extracted from sqsMessage and sqsMessage.Message
type ctMessage struct {
	S3Bucket      string   `json:"s3Bucket"`
	S3ObjectKey   []string `json:"s3ObjectKey"`
	MessageID     string   `json:",omitempty"`
	ReceiptHandle string   `json:",omitempty"`
}

// data struct matching the defined fields of a CloudTrail Record as
//  described in:
//  http://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-event-reference-record-contents.html
type cloudtrailLog struct {
	Records []cloudtrailEvent
}
type cloudtrailEvent struct {
	EventTime          string                 `json:"eventTime"`
	EventVersion       string                 `json:"eventVersion"`
	EventSource        string                 `json:"eventSource"`
	UserIdentity       map[string]interface{} `json:"userIdentity"`
	EventName          string                 `json:"eventName"`
	AwsRegion          string                 `json:"awsRegion"`
	SourceIPAddress    string                 `json:"sourceIPAddress"`
	UserAgent          string                 `json:"userAgent"`
	ErrorCode          string                 `json:"errorCode"`
	ErrorMessage       string                 `json:"errorMessage,omitempty"`
	RequestParameters  map[string]interface{} `json:"requestParamteres"`
	ResponseElements   map[string]interface{} `json:"responseElements"`
	RequestID          string                 `json:"requestID"`
	EventID            string                 `json:"eventID"`
	EventType          string                 `json:"eventType"`
	APIVersion         string                 `json:"apiVersion"`
	RecipientAccountID string                 `json:"recipientAccountID"`
}

func init() {
	cmdLineArgs = CmdLineArgs{
		backfillBucket: flag.String("b", "", "Name of S3 bucket used for backfilling"),
		backfillPrefix: flag.String("p", "", "Prefix to be used when listing objects from S3 bucket"),
	}
}

func New() *CloudTrailbeat {
	cb := &CloudTrailbeat{}
	cb.CmdLineArgs = cmdLineArgs

	return cb
}

func (cb *CloudTrailbeat) Config(b *beat.Beat) error {
	if err := cfgfile.Read(&cb.CTbConfig, ""); err != nil {
		logp.Err("Error reading configuration file: %v", err)
		return err
	}

	//Validate and instantiate configuration file variables
	if cb.CTbConfig.Input.SQSUrl != nil {
		cb.sqsURL = *cb.CTbConfig.Input.SQSUrl
	} else {
		return errors.New("Invalid SQS URL in configuration file")
	}

	if cb.CTbConfig.Input.NumQueueFetch != nil {
		cb.numQueueFetch = *cb.CTbConfig.Input.NumQueueFetch
	} else {
		cb.numQueueFetch = 1
	}

	if cb.CTbConfig.Input.SleepTime != nil {
		cb.sleepTime = time.Duration(*cb.CTbConfig.Input.SleepTime) * time.Second
	} else {
		cb.sleepTime = time.Minute * 5
	}

	if cb.CTbConfig.Input.NoPurge != nil {
		cb.noPurge = *cb.CTbConfig.Input.NoPurge
	} else {
		cb.noPurge = false
	}

	// use AWS credentials from configuration file if provided, fall back to ENV and ~/.aws/credentials
	if cb.CTbConfig.Input.AWSCredentialProvider != nil {
		cb.awsConfig = &aws.Config{
			Credentials: credentials.NewSharedCredentials("", "cb.CTbConfig.Input.AWSCredentialProvider"),
		}
	} else {
		cb.awsConfig = aws.NewConfig()
	}

	if cb.CTbConfig.Input.AWSRegion != nil {
		cb.awsConfig = cb.awsConfig.WithRegion(*cb.CTbConfig.Input.AWSRegion)
	}

	// parse cmd line flags to determine if backfill or queue mode is being used
	if cb.CmdLineArgs.backfillBucket != nil {
		cb.backfillBucket = *cb.CmdLineArgs.backfillBucket

		if cb.CmdLineArgs.backfillPrefix != nil {
			cb.backfillPrefix = *cb.CmdLineArgs.backfillPrefix
		}
	}

	logp.Debug("cloudtrailbeat", "Init cloudtrailbeat")
	logp.Debug("cloudtrailbeat", "SQS Url: %s", cb.sqsURL)
	logp.Debug("cloudtrailbeat", "Number of items to fetch from queue: %d", cb.numQueueFetch)
	logp.Debug("cloudtrailbeat", "Time to sleep when queue is empty: %.0f", cb.sleepTime.Seconds())
	logp.Debug("cloudtrailbeat", "Events will be deleted from SQS when processed: %t", cb.noPurge)
	logp.Debug("cloudtrailbeat", "Backfill bucket: %s", cb.backfillBucket)
	logp.Debug("cloudtrailbeat", "Backfill prefix: %s", cb.backfillPrefix)

	return nil
}

func (cb *CloudTrailbeat) Setup(b *beat.Beat) error {
	cb.events = b.Events
	cb.done = make(chan struct{})
	return nil
}

func (cb *CloudTrailbeat) Run(b *beat.Beat) error {
	if cb.backfillBucket != "" {
		if err := cb.runBackfill(); err != nil {
			logp.Err("Error backfilling logs: %s", err)
			os.Exit(1)
		}
	} else {
		if err := cb.runQueue(); err != nil {
			logp.Err("Error processing queue: %s", err)
			os.Exit(1)
		}
	}
	return nil
}

func (cb *CloudTrailbeat) runQueue() error {
	for {
		select {
		case <-cb.done:
			return nil
		default:
		}

		messages, err := cb.fetchMessages()
		if err != nil {
			logp.Err("Error fetching messages from SQS: %v", err)
			break
		}

		if len(messages) == 0 {
			logp.Info("No new events to process, sleeping for %.0f seconds", cb.sleepTime.Seconds())
			time.Sleep(cb.sleepTime)
			continue
		}

		logp.Info("Fetched %d new CloudTrail events from SQS.", len(messages))
		// fetch and process each log file
		for _, m := range messages {
			logp.Info("Downloading and processing log file: s3://%s/%s", m.S3Bucket, m.S3ObjectKey)
			lf, err := cb.readLogfile(m)
			if err != nil {
				logp.Err("Error reading log file [id: %s]: %s", m.MessageID, err)
				continue
			}

			if err := cb.publishEvents(lf); err != nil {
				logp.Err("Error publishing CloudTrail events [id: %s]: %s", m.MessageID, err)
				continue
			}
			if !cb.noPurge {
				if err := cb.deleteMessage(m); err != nil {
					logp.Err("Error deleting proccessed SQS event [id: %s]: %s", m.MessageID, err)
				}
			}
			logp.Info("Successfully published %d new events", len(lf.Records))
		}
	}

	return nil
}

func (cb *CloudTrailbeat) runBackfill() error {
	s := s3.New(session.New(cb.awsConfig))
	q := s3.ListObjectsInput{
		Bucket: aws.String(cb.backfillBucket),
		Prefix: aws.String(cb.backfillPrefix),
	}

	if list, err := s.ListObjects(&q); err == nil {
		for _, e := range list.Contents {
			if err := cb.pushQueue(cb.backfillBucket, *e.Key); err != nil {
				return fmt.Errorf("Queue push failed: %s", err)
			}
		}
	} else {
		return fmt.Errorf("Failed to list bucket objects: %s", err)
	}
	return nil
}

func (cb *CloudTrailbeat) pushQueue(bucket, key string) error {
	body := ctMessage{
		S3Bucket:    bucket,
		S3ObjectKey: []string{key},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}

	msg := sqsMessage{Message: string(b)}
	m, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	q := sqs.New(session.New(cb.awsConfig))
	_, err = q.SendMessage(&sqs.SendMessageInput{
		QueueUrl:    aws.String(cb.sqsURL),
		MessageBody: aws.String(string(m)),
	})
	if err != nil {
		return err
	}

	return nil
}

func (cb *CloudTrailbeat) Stop() {
	close(cb.done)
}

func (cb *CloudTrailbeat) Cleanup(b *beat.Beat) error {
	return nil
}

func (cb *CloudTrailbeat) publishEvents(ct cloudtrailLog) error {
	if len(ct.Records) < 1 {
		return nil
	}

	events := make([]common.MapStr, 0, len(ct.Records))

	for _, cte := range ct.Records {
		timestamp, err := time.Parse(logTimeFormat, cte.EventTime)
		if err != nil {
			logp.Err("Unable to parse EventTime : %s", cte.EventTime)
		}

		be := common.MapStr{
			"@timestamp": common.Time(timestamp),
			"type":       "CloudTrail",
			"cloudtrail": cte,
		}

		events = append(events, be)
	}
	if !cb.events.PublishEvents(events, publisher.Sync, publisher.Guaranteed) {
		return fmt.Errorf("Error publishing events")
	}

	return nil
}

func (cb *CloudTrailbeat) readLogfile(m ctMessage) (cloudtrailLog, error) {
	events := cloudtrailLog{}

	s := s3.New(session.New(cb.awsConfig))
	q := s3.GetObjectInput{
		Bucket: aws.String(m.S3Bucket),
		Key:    aws.String(m.S3ObjectKey[0]),
	}
	o, err := s.GetObject(&q)
	if err != nil {
		return events, err
	}
	b, err := ioutil.ReadAll(o.Body)
	if err != nil {
		return events, err
	}

	if err := json.Unmarshal(b, &events); err != nil {
		return events, fmt.Errorf("Error unmarshaling cloutrail JSON: %s", err.Error())
	}

	return events, nil
}

func (cb *CloudTrailbeat) fetchMessages() ([]ctMessage, error) {
	var m []ctMessage

	q := sqs.New(session.New(cb.awsConfig))
	params := &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(cb.sqsURL),
		MaxNumberOfMessages: aws.Int64(int64(cb.numQueueFetch)),
	}

	resp, err := q.ReceiveMessage(params)
	if err != nil {
		return m, fmt.Errorf("SQS ReceiveMessage error: %s", err.Error())
	}

	//no new meesages in queue
	if len(resp.Messages) == 0 {
		return nil, nil
	}

	for _, e := range resp.Messages {
		tmsg := sqsMessage{}
		if err := json.Unmarshal([]byte(*e.Body), &tmsg); err != nil {
			return nil, fmt.Errorf("SQS message JSON parse error [id: %s]: %s", *e.MessageId, err.Error())
		}

		event := ctMessage{}
		if err := json.Unmarshal([]byte(tmsg.Message), &event); err != nil {
			return nil, fmt.Errorf("SQS body JSON parse error [id: %s]: %s", *e.MessageId, err.Error())
		}

		if tmsg.Message == "CloudTrail validation message." {
			if !cb.noPurge {
				if err := cb.deleteMessage(event); err != nil {
					return nil, fmt.Errorf("Error deleting 'validation message' [id: %s]: %s", tmsg.MessageID, err)
				}
			}
			continue
		}

		event.MessageID = tmsg.MessageID
		event.ReceiptHandle = *e.ReceiptHandle

		m = append(m, event)
	}

	return m, nil
}

func (cb *CloudTrailbeat) deleteMessage(m ctMessage) error {
	q := sqs.New(session.New(cb.awsConfig))
	params := &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(cb.sqsURL),
		ReceiptHandle: aws.String(m.ReceiptHandle),
	}

	_, err := q.DeleteMessage(params)
	if err != nil {
		return err
	}

	return nil
}
