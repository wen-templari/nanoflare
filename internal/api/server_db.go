package api

import (
	"errors"
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type dbExecuteInput struct {
	SQL        string                         `json:"sql"`
	Name       string                         `json:"name,omitempty"`
	Statements []nanoflare.DBStatementRequest `json:"statements,omitempty"`
}

func (s *Server) registerDBRoutes() {
	s.mux.HandleFunc("GET /v1/db", s.listDatabases)
	s.mux.HandleFunc("POST /v1/db", s.createDatabase)
	s.mux.HandleFunc("DELETE /v1/db/{databaseID}", s.deleteDatabase)
	s.mux.HandleFunc("GET /v1/db/{databaseID}/metrics", s.databaseMetrics)
	s.mux.HandleFunc("GET /v1/db/{databaseID}/metrics/timeseries", s.databaseMetricsTimeseries)
	s.mux.HandleFunc("POST /v1/db/{databaseID}/execute", s.executeDatabase)
	s.mux.HandleFunc("POST /v1/db/{databaseID}/migrations", s.applyDatabaseMigration)
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
