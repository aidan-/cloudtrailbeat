# CloudTrailBeat
Current status: **beta release**

## Description
This is a beat for [Amazon Web Services (AWS) CloudTrail](https://aws.amazon.com/cloudtrail/) service.  CloudTrailBeat relies on a combination of the SNS and SQS services to create a processing 'pipeline' to able to process new log events quickly and efficiently.  The beat polls the SQS service and downloads new log files from S3 when they are made available.  Each log file is processed and shipped to the configured receiver.  Users can configure SQS polling intervals to suit individual requirements.

## Building
These steps assume you already have a working [Go environment](https://golang.org/doc/install).

```bash
$GOPATH
mkdir -p src/github.com/aidan-
cd src/github.com/aidan-
git clone https://github.com/aidan-/cloudtrailbeat.git
cd cloudtrailbeat
make
```

## Documentation
1. AWS configuration
2. CloudTrailbeat queue polling
3. CloudTrailbeat backfilling

## Thanks
This beat is heavily inspired by [AppliedTrust/traildash](https://github.com/AppliedTrust/traildash) with some updates and additional functionality.

## Todo
- Usage documentation including neccessary AWS configuration
- Test cases
- Example Kibana configurations and Elasticsearch templates
