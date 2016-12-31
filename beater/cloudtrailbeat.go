package beater

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/sqs"

	"github.com/elastic/beats/libbeat/beat"
	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/logp"
	"github.com/elastic/beats/libbeat/publisher"

	"github.com/aidan-/cloudtrailbeat/config"
)

const logTimeFormat = "2006-01-02T15:04:05Z"

var (
	backfillBucket = flag.String("b", "", "Name of S3 bucket used for backfilling")
	backfillPrefix = flag.String("p", "", "Prefix to be used when listing objects from S3 bucket")
)

type Cloudtrailbeat struct {
	done   chan struct{}
	config config.Config
	client publisher.Client

	awsConfig      *aws.Config
	backfillBucket string
	backfillPrefix string
}

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
	RequestID          string                 `json:"requestID"`
	EventID            string                 `json:"eventID"`
	EventType          string                 `json:"eventType"`
	APIVersion         string                 `json:"apiVersion"`
	RecipientAccountID string                 `json:"recipientAccountID"`

	RequestParameters    json.RawMessage `json:"requestParameters,omitempty"`
	ResponseElements     json.RawMessage `json:"responseElements,omitempty"`
	RawRequestParameters string          `json:"rawRequestParameters"`
	RawResponseElements  string          `json:"rawResponseElements"`
}

// Creates beater
func New(b *beat.Beat, cfg *common.Config) (beat.Beater, error) {
	config := config.DefaultConfig
	if err := cfg.Unpack(&config); err != nil {
		return nil, fmt.Errorf("Error reading config file: %v", err)
	}

	bt := &Cloudtrailbeat{
		done:   make(chan struct{}),
		config: config,
	}
	return bt, nil
}

func (bt *Cloudtrailbeat) Run(b *beat.Beat) error {
	logp.Info("cloudtrailbeat is running! Hit CTRL-C to stop it.")

	bt.client = b.Publisher.Connect()
	// Configure AWS client
	bt.awsConfig = aws.NewConfig().WithRegion(bt.config.AWSRegion)

	if *backfillBucket != "" {
		logp.Info("Running in backfill mode.")
		if err := bt.runBackfill(*backfillBucket, *backfillPrefix); err != nil {
			return fmt.Errorf("Error backfilling logs: %s", err)
		}
	} else {
		logp.Info("Running in queue mode")
		if err := bt.runQueue(); err != nil {
			return fmt.Errorf("Error processing queue: %s", err)
		}
	}

	return nil
}

func (bt *Cloudtrailbeat) runQueue() error {
	for {
		select {
		case <-bt.done:
			return nil
		default:
		}

		messages, err := bt.fetchMessages()
		if err != nil {
			logp.Err("Error fetching messages from SQS: %v", err)
			break
		}

		if len(messages) == 0 {
			logp.Info("No new messages to process, sleeping for %.0f seconds", bt.config.Period.Seconds())
			time.Sleep(bt.config.Period)
			continue
		}

		logp.Info("Fetched %d new CloudTrail messages from SQS.", len(messages))
		// fetch each CloudTrail log file and process
		for _, m := range messages {
			logp.Info("Downloading and processing log file: s3://%s/%s/", m.S3Bucket, m.S3ObjectKey)
			lf, err := bt.readLogfile(m)
			if err != nil {
				logp.Err("Error downloading or processing log file [id: %s]: %s", m.MessageID, err)
				continue
			}

			if err := bt.publishEvents(lf); err != nil {
				logp.Err("Error publishing CloudTrail events [id: %s]: %s", m.MessageID, err)
				continue
			}
			if !bt.config.NoPurge {
				if err := bt.deleteMessage(m.ReceiptHandle); err != nil {
					logp.Err("Error deleting proccessed SQS event [id: %s]: %s", m.MessageID, err)
				}
			}
		}
	}

	return nil
}

func (bt *Cloudtrailbeat) Stop() {
	bt.client.Close()
	close(bt.done)
}

func (bt *Cloudtrailbeat) pushQueue(bucket, key string) error {
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

	q := sqs.New(session.New(bt.awsConfig))
	_, err = q.SendMessage(&sqs.SendMessageInput{
		QueueUrl:    aws.String(bt.config.SQSURL),
		MessageBody: aws.String(string(m)),
	})
	if err != nil {
		return err
	}

	return nil
}

func (bt *Cloudtrailbeat) publishEvents(ct cloudtrailLog) error {
	if len(ct.Records) < 1 {
		return nil
	}

	events := make([]common.MapStr, 0, len(ct.Records))

	for _, cte := range ct.Records {
		timestamp, err := time.Parse(logTimeFormat, cte.EventTime)
		if err != nil {
			logp.Err("Unable to parse EventTime : %s", cte.EventTime)
		}

		//as libbeat doesn't pass the message to json.Marshal as a pointer we need to do this to ensure the RawMessage fields are not base64 encoded.
		cte.RawRequestParameters = string(cte.RequestParameters[:])
		cte.RawResponseElements = string(cte.ResponseElements[:])
		cte.RequestParameters = nil
		cte.ResponseElements = nil

		be := common.MapStr{
			"@timestamp": common.Time(timestamp),
			"type":       "CloudTrail",
			"cloudtrail": cte,
		}

		events = append(events, be)
	}
	if !bt.client.PublishEvents(events, publisher.Sync, publisher.Guaranteed) {
		return fmt.Errorf("Error publishing events")
	}

	return nil
}

func (bt *Cloudtrailbeat) readLogfile(m ctMessage) (cloudtrailLog, error) {
	events := cloudtrailLog{}

	s := s3.New(session.New(bt.awsConfig))
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

func (bt *Cloudtrailbeat) fetchMessages() ([]ctMessage, error) {
	var m []ctMessage

	q := sqs.New(session.New(bt.awsConfig))
	params := &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(bt.config.SQSURL),
		MaxNumberOfMessages: aws.Int64(int64(bt.config.NumQueueFetch)),
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

		// delete cloudtrail test message
		if tmsg.Message == "CloudTrail validation message." {
			if !bt.config.NoPurge {
				if err := bt.deleteMessage(*e.ReceiptHandle); err != nil {
					return nil, fmt.Errorf("Error deleting 'validation message' [id: %s]: %s", tmsg.MessageID, err)
				}
			}
			continue
		}

		event := ctMessage{}
		if err := json.Unmarshal([]byte(tmsg.Message), &event); err != nil {
			return nil, fmt.Errorf("SQS body JSON parse error [id: %s]: %s", *e.MessageId, err.Error())

		}

		event.MessageID = tmsg.MessageID
		event.ReceiptHandle = *e.ReceiptHandle

		m = append(m, event)
	}

	return m, nil
}

func (bt *Cloudtrailbeat) deleteMessage(rh string) error {
	q := sqs.New(session.New(bt.awsConfig))
	params := &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(bt.config.SQSURL),
		ReceiptHandle: aws.String(rh),
	}

	_, err := q.DeleteMessage(params)
	if err != nil {
		return err
	}

	return nil
}

func (bt *Cloudtrailbeat) runBackfill(bucket, prefix string) error {
	logp.Info("Backfilling using S3 bucket: s3://%s/%s", bucket, prefix)

	s := s3.New(session.New(bt.awsConfig))
	q := s3.ListObjectsInput{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	}

	if list, err := s.ListObjects(&q); err == nil {
		for _, e := range list.Contents {
			if strings.HasSuffix(*e.Key, ".json.gz") {
				logp.Info("Found log file to add to queue: %s", *e.Key)
				if err := bt.pushQueue(bucket, *e.Key); err != nil {
					logp.Err("Failed to push log file onto queue: %s", err)
					return fmt.Errorf("Queue push failed: %s", err)
				}
			}
		}
	} else {
		logp.Err("Unable to list objects in bucket: %s", err)
		return fmt.Errorf("Failed to list bucket objects: %s", err)
	}
	return nil
}
