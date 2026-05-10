package main

import (
	"database/sql"
	"errors"
	"fmt"
)

type taskNotFoundError struct {
	taskID string
	err    error
}

func (e taskNotFoundError) Error() string { return fmt.Sprintf("task %s not found", e.taskID) }
func (e taskNotFoundError) Unwrap() error { return e.err }

func taskLookupError(taskID string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return taskNotFoundError{taskID: taskID, err: err}
	}
	return err
}

func runLookupError(taskID string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %s has no runs", taskID)
	}
	return err
}
