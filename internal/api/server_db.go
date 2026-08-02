package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type dbExecuteInput struct {
	SQL        string                         `json:"sql"`
	Name       string                         `json:"name,omitempty"`
	Statements []nanoflare.DBStatementRequest `json:"statements,omitempty"`
}

func (s *Server) registerDBRoutes() {
	base := "/v1/organizations/{orgID}/databases"
	s.mux.HandleFunc("GET "+base, s.listDatabases)
	s.mux.HandleFunc("GET "+base+"/metrics", s.listDatabaseMetrics)
	s.mux.HandleFunc("POST "+base, s.createDatabase)
	s.mux.HandleFunc("DELETE "+base+"/{databaseID}", s.deleteDatabase)
	s.mux.HandleFunc("GET "+base+"/{databaseID}/analytics", s.databaseMetricsTimeseries)
	s.mux.HandleFunc("GET "+base+"/{databaseID}/replication", s.databaseReplicationStatus)
	s.mux.HandleFunc("POST "+base+"/{databaseID}/restore", s.restoreDatabase)
	s.mux.HandleFunc("POST "+base+"/{databaseID}/queries", s.executeDatabase)
	s.mux.HandleFunc("POST "+base+"/{databaseID}/migrations", s.applyDatabaseMigration)
}

func (s *Server) listDatabaseMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:read") {
		return
	}
	metrics, err := s.service.DatabaseMetricsListForOrg(controlOrgID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (s *Server) databaseReplicationStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:read") {
		return
	}
	status, err := s.service.DatabaseReplicationStatusForOrg(controlOrgID(r), r.PathValue("databaseID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) restoreDatabase(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:write") {
		return
	}
	var input nanoflare.RestoreDatabaseInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var timestamp *time.Time
	if value := strings.TrimSpace(input.Timestamp); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("timestamp must be RFC3339"))
			return
		}
		utc := parsed.UTC()
		timestamp = &utc
	}
	result, err := s.service.RestoreDatabaseForOrg(controlOrgID(r), r.PathValue("databaseID"), timestamp)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listDatabases(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:read") {
		return
	}
	databases, err := s.service.ListDatabasesForOrg(controlOrgID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, databases)
}

func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:write") {
		return
	}
	var input nanoflare.CreateDatabaseInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.OrgID = controlOrgID(r)
	database, err := s.service.CreateDatabase(input)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, database)
}

func (s *Server) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:write") {
		return
	}
	if err := s.service.DeleteDatabaseForOrg(controlOrgID(r), r.PathValue("databaseID")); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) databaseMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:read") {
		return
	}
	metrics, err := s.service.DatabaseMetricsForOrg(controlOrgID(r), r.PathValue("databaseID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (s *Server) databaseMetricsTimeseries(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:read") {
		return
	}
	series, err := s.service.DatabaseMetricsTimeseriesForOrg(controlOrgID(r), r.PathValue("databaseID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, series)
}

func (s *Server) executeDatabase(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:write") {
		return
	}
	var input dbExecuteInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request := nanoflare.DBQueryRequest{Method: "exec", Statements: []nanoflare.DBStatementRequest{{SQL: input.SQL}}}
	if len(input.Statements) > 0 {
		request.Method = "batch"
		request.Statements = input.Statements
	}
	response, err := s.service.WorkerDBExecuteForOrg(controlOrgID(r), r.PathValue("databaseID"), request)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) applyDatabaseMigration(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "db:write") {
		return
	}
	var input dbExecuteInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Name == "" {
		writeError(w, http.StatusBadRequest, errors.New("migration name is required"))
		return
	}
	result, err := s.service.ApplyDBMigrationForOrg(controlOrgID(r), r.PathValue("databaseID"), input.Name, input.SQL)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
