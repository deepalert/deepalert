package main

import (
	"encoding/json"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/deepalert/deepalert"
	"github.com/deepalert/deepalert/internal/errors"
	"github.com/deepalert/deepalert/internal/handler"
	"github.com/deepalert/deepalert/internal/logging"
)

var logger = logging.Logger

func main() {
	handler.StartLambda(handleRequest)
}

func handleRequest(args *handler.Arguments) (handler.Response, error) {
	snsSvc := args.SNSService()
	repo, err := args.Repository()
	if err != nil {
		return nil, err
	}

	now := time.Now()

	sqsMessages, err := args.DecapSQSEvent()
	if err != nil {
		return nil, err
	}

	for _, msg := range sqsMessages {
		var reportedAttr deepalert.ReportAttribute
		if err := json.Unmarshal(msg, &reportedAttr); err != nil {
			return nil, errors.Wrap(err, "Unmarshal ReportAttribute").With("msg", string(msg))
		}

		logger.WithField("reportedAttr", reportedAttr).Info("unmarshaled reported attribute")

		for _, attr := range reportedAttr.Attributes {
			sendable, err := repo.PutAttributeCache(reportedAttr.ReportID, *attr, now)
			if err != nil {
				return nil, errors.Wrap(err, "Fail to manage attribute cache").With("attr", attr)
			}

			logger.WithFields(logrus.Fields{"sendable": sendable, "attr": attr}).Info("attribute")
			if !sendable {
				continue
			}

			task := deepalert.Task{
				ReportID:  reportedAttr.ReportID,
				Attribute: attr,
			}

			if err := snsSvc.Publish(args.TaskTopic, &task); err != nil {
				return nil, err
			}
		}
	}

	return nil, nil
}
