package storage

import (
	"database/sql"

	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/kubeflow/pipelines/backend/src/apiserver/common"
	"github.com/kubeflow/pipelines/backend/src/apiserver/list"
	"github.com/kubeflow/pipelines/backend/src/apiserver/model"
	"github.com/kubeflow/pipelines/backend/src/common/util"
)

type ExperimentStoreInterface interface {
	ListExperiments(opts *list.Options) ([]*model.Experiment, string, error)
	GetExperiment(uuid string) (*model.Experiment, error)
	CreateExperiment(*model.Experiment) (*model.Experiment, error)
	DeleteExperiment(uuid string) error
}

type ExperimentStore struct {
	db                     *DB
	time                   util.TimeInterface
	uuid                   util.UUIDGeneratorInterface
	resourceReferenceStore *ResourceReferenceStore
}

func (s *ExperimentStore) ListExperiments(opts *list.Options) ([]*model.Experiment, string, error) {
	errorF := func(err error) ([]*model.Experiment, string, error) {
		return nil, "", util.NewInternalServerError(err, "Failed to list experiments: %v", err)
	}

	sql, args, err := opts.AddToSelect(sq.Select("*").From("experiments")).ToSql()
	if err != nil {
		return errorF(err)
	}

	rows, err := s.db.Query(sql, args...)
	if err != nil {
		return errorF(err)
	}
	defer rows.Close()

	exps, err := s.scanRows(rows)
	if err != nil {
		return errorF(err)
	}

	if len(exps) <= opts.PageSize {
		return exps, "", nil
	}

	npt, err := opts.NextPageToken(exps[opts.PageSize])
	return exps[:opts.PageSize], npt, err
}

func (s *ExperimentStore) GetExperiment(uuid string) (*model.Experiment, error) {
	sql, args, err := sq.
		Select("*").
		From("experiments").
		Where(sq.Eq{"uuid": uuid}).
		Limit(1).
		ToSql()
	if err != nil {
		return nil, util.NewInternalServerError(err, "Failed to get experiment: %v", err.Error())
	}
	r, err := s.db.Query(sql, args...)
	if err != nil {
		return nil, util.NewInternalServerError(err, "Failed to get experiment: %v", err.Error())
	}
	defer r.Close()
	experiments, err := s.scanRows(r)

	if err != nil || len(experiments) > 1 {
		return nil, util.NewInternalServerError(err, "Failed to get experiment: %v", err.Error())
	}
	if len(experiments) == 0 {
		return nil, util.NewResourceNotFoundError("Experiment", fmt.Sprint(uuid))
	}
	return experiments[0], nil
}

func (s *ExperimentStore) scanRows(rows *sql.Rows) ([]*model.Experiment, error) {
	var experiments []*model.Experiment
	for rows.Next() {
		var uuid, name, description string
		var createdAtInSec int64
		err := rows.Scan(&uuid, &name, &description, &createdAtInSec)
		if err != nil {
			return experiments, nil
		}
		experiments = append(experiments, &model.Experiment{
			UUID:           uuid,
			Name:           name,
			Description:    description,
			CreatedAtInSec: createdAtInSec,
		})
	}
	return experiments, nil
}

func (s *ExperimentStore) CreateExperiment(experiment *model.Experiment) (*model.Experiment, error) {
	newExperiment := *experiment
	now := s.time.Now().Unix()
	newExperiment.CreatedAtInSec = now
	id, err := s.uuid.NewRandom()
	if err != nil {
		return nil, util.NewInternalServerError(err, "Failed to create an experiment id.")
	}
	newExperiment.UUID = id.String()
	sql, args, err := sq.
		Insert("experiments").
		SetMap(
			sq.Eq{
				"UUID":           newExperiment.UUID,
				"CreatedAtInSec": newExperiment.CreatedAtInSec,
				"Name":           newExperiment.Name,
				"Description":    newExperiment.Description}).
		ToSql()
	if err != nil {
		return nil, util.NewInternalServerError(err, "Failed to create query to insert experiment to experiment table: %v",
			err.Error())
	}
	_, err = s.db.Exec(sql, args...)
	if err != nil {
		if s.db.IsDuplicateError(err) {
			return nil, util.NewInvalidInputError(
				"Failed to create a new experiment. The name %v already exist. Please specify a new name.", experiment.Name)
		}
		return nil, util.NewInternalServerError(err, "Failed to add experiment to experiment table: %v",
			err.Error())
	}
	return &newExperiment, nil
}

func (s *ExperimentStore) DeleteExperiment(id string) error {
	experimentSql, experimentArgs, err := sq.Delete("experiments").Where(sq.Eq{"UUID": id}).ToSql()
	if err != nil {
		return util.NewInternalServerError(err,
			"Failed to create query to delete experiment: %s", id)
	}
	// Use a transaction to make sure both experiment and its resource references are stored.
	tx, err := s.db.Begin()
	if err != nil {
		return util.NewInternalServerError(err, "Failed to create a new transaction to delete experiment.")
	}
	_, err = tx.Exec(experimentSql, experimentArgs...)
	if err != nil {
		tx.Rollback()
		return util.NewInternalServerError(err, "Failed to delete experiment %s from table", id)
	}
	err = s.resourceReferenceStore.DeleteResourceReferences(tx, id, common.Run)
	if err != nil {
		tx.Rollback()
		return util.NewInternalServerError(err, "Failed to delete resource references from table for experiment %v ", id)
	}
	err = tx.Commit()
	if err != nil {
		tx.Rollback()
		return util.NewInternalServerError(err, "Failed to delete experiment %v and its resource references from table", id)
	}
	return nil
}

// factory function for experiment store
func NewExperimentStore(db *DB, time util.TimeInterface, uuid util.UUIDGeneratorInterface) *ExperimentStore {
	return &ExperimentStore{db: db, time: time, uuid: uuid, resourceReferenceStore: NewResourceReferenceStore(db)}
}
