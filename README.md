# CloudTrailBeat
Current status: **beta release**

## Overview
This is a beat for the [Amazon Web Services (AWS) CloudTrail](https://aws.amazon.com/cloudtrail/) service.  CloudTrailBeat relies on a combination of SNS, SQS and S3 to create a processing 'pipeline' to process new log events quickly and efficiently.  The beat polls the SQS queue for notification of when a new CloudTrail log file is available for download in S3.  Each log file is then downloaded, processed and sent to the configured receiver (logstash, elasticsearch, etc).  You are then able to query the data using Kibana (or any other tool) to analyse events involving API calls and IAM authentications.

## Getting Started
### Requirements
* [Golang](https://golang.org/dl/) 1.6
* [Glide](https://github.com/Masterminds/glide) >= 0.10.0

### Building
These steps assume you already have a working [Go environment](https://golang.org/doc/install).

```bash
git clone https://github.com/aidan-/cloudtrailbeat.git
cd cloudtrailbeat
glide install
make
```

### AWS Configuration
#### Pipeline configuration
Confguring CloudTrail is relatively straight forward and can be done quite easily through the AWS web console.  The [official documentation](http://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-create-and-update-a-trail.html) outlines the steps required to configure everything, just ensure you complete the optional step 3.

If you would prefer to use CloudFormation to configure your environment, you can use the [provided template](conf/cloudtrail_cf.template) which will configure all of the neccessary services (CloudTrail, S3, SQS).   

Once configured, you can confirm everything is working by inspecting the configured S3 bucket as well as the SQS queue.

#### Access control configuration
CloudTrailBeat supports usage of both IAM roles and API keys, but as per AWS best practices, if CloudTrailBeat is being run from an EC2 you should be using IAM roles.  The following IAM Policy provides the minimal access required to process new CloudTrail events and initiate backfilling.  Make sure you replace the S3 and SQS ARN's with the values appropriate to your configuration.

```JSON
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "AllowS3BucketAccess",
            "Effect": "Allow",
            "Action": [
                "s3:ListBucket"
            ],
            "Resource": [
                "arn:aws:s3:::<bucket name here>"
            ]
        },
        {
            "Sid": "AllowObjectRetrieval",
            "Effect": "Allow",
            "Action": [
                "s3:GetObject"
            ],
            "Resource": [
                "arn:aws:s3:::<bucket name here>/*"
            ]
        },
        {
            "Sid": "AllowSQS",
            "Effect": "Allow",
            "Action": [
                "sqs:DeleteMessage",
                "sqs:ReceiveMessage",
                "sqs:SendMessage"
            ],
            "Resource": [
                "arn:aws:sqs:<sqs arn here>"
            ]
        }
    ]
}
```

## Thanks
This beat is heavily inspired by [AppliedTrust/traildash](https://github.com/AppliedTrust/traildash) with some updates and additional functionality.

## Todo
- Test cases
- Example Kibana configurations and Elasticsearch templates
