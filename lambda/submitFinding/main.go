package main

import (
	"encoding/json"
	"time"

	"github.com/deepalert/deepalert"
	"github.com/deepalert/deepalert/internal/handler"
	"github.com/m-mizutani/golambda"
)

var logger = golambda.Logger

func main() {
	golambda.Start(func(event golambda.Event) (interface{}, error) {
		args := handler.NewArguments()
		if err := args.BindEnvVars(); err != nil {
			return nil, err
		}

		if err := handleRequest(args, event); err != nil {
			return nil, err
		}
		return nil, nil
	})
}

func handleRequest(args *handler.Arguments, event golambda.Event) error {
	messages, err := event.DecapSQSBody()
	if err != nil {
		return err
	}

	repo, err := args.Repository()
	if err != nil {
		return err
	}
	now := time.Now()

	for _, msg := range messages {
		var ir deepalert.Finding
		if err := json.Unmarshal(msg, &ir); err != nil {
			return golambda.WrapError(err, "Fail to unmarshal Finding from SubmitNotification").With("msg", string(msg))
		}
		logger.With("inspectReport", ir).Debug("Handling inspect report")

		if err := repo.SaveFinding(ir, now); err != nil {
			return golambda.WrapError(err, "Fail to save Finding").With("report", ir)
		}
		logger.With("section", ir).Info("Saved content")
	}

	return nil
}
