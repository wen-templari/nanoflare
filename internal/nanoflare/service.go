package nanoflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var runtimeResolutionTracer = otel.Tracer("github.com/clas/nanoflare/internal/nanoflare/runtime-resolution")

type ConfigWriter interface {
	Write([]ActiveDeployment) error
}

type ObjectStore interface {
	PresignUpload(appID, path string, expiry time.Duration) (string, error)
	PresignDownload(appID, path string, expiry time.Duration) (string, error)
	Put(appID, path string, contentType string, data []byte) (ObjectInfo, error)
	Get(appID, path string) (ObjectBody, error)
	Head(appID, path string) (ObjectInfo, error)
	List(appID, prefix string) ([]ObjectInfo, error)
	Delete(appID, path string) error
}

type WorkerOutputReader interface {
	Output(string) []WorkerOutputLine
}

type WorkerOutputQuery struct {
	DeploymentID string
	Level        string
	Text         string
	Since        time.Time
	Until        time.Time
	Limit        int
}

type WorkerOutputQuerier interface {
	Query(context.Context, string, WorkerOutputQuery) ([]WorkerOutputLine, error)
}

type WorkerTrafficReader interface {
	Traffic(string) (WorkerTraffic, error)
}

type DatabaseMetricsTimeseriesReader interface {
	DatabaseMetricsTimeseries(string) (DatabaseMetricsTimeseries, error)
}

type KVNamespaceMetricsTimeseriesReader interface {
	KVNamespaceMetricsTimeseries(string) (KVNamespaceMetricsTimeseries, error)
}
type ObjectStorageBucketMetricsTimeseriesReader interface {
	ObjectStorageBucketMetricsTimeseries(string) (ObjectStorageBucketMetricsTimeseries, error)
}
type WorkerMetricsTimeseriesReader interface {
	WorkerMetricsTimeseries(string) (WorkerMetricsTimeseries, error)
}
type OrganizationWorkerMetricsTimeseriesReader interface {
	OrganizationWorkerMetricsTimeseries([]string) (WorkerMetricsTimeseries, error)
}

type RepositoryPoolStatsReader interface {
	PoolStats() RepositoryPoolStats
}

type kvValueSizer interface {
	KVValueSize(capability, namespaceID, key string) (int64, bool, error)
}

type kvWriteOptimizer interface {
	KVPutWithSizeDelta(capability, namespaceID, key string, value []byte, options ...RuntimeKVPutOptions) (int64, error)
	KVDeleteWithSizeDelta(capability, namespaceID, key string) (int64, error)
}

type kvOrgUsageCacheDisabler interface {
	disableKVOrgUsageCache() bool
}

type Service struct {
	dispatch             dispatchRuntime
	telemetry            telemetryState
	runtimeAuthorization runtimeAuthorization
	// Serialize configuration snapshots and publication, including rollbacks.
	configMu             sync.Mutex
	store                Repository
	writer               ConfigWriter
	objects              ObjectStore
	db                   DBExecutor
	output               WorkerOutputReader
	outputQueries        chan struct{}
	traffic              WorkerTrafficReader
	dbTimeseries         DatabaseMetricsTimeseriesReader
	kvTimeseries         KVNamespaceMetricsTimeseriesReader
	objectTimeseries     ObjectStorageBucketMetricsTimeseriesReader
	workerTimeseries     WorkerMetricsTimeseriesReader
	secrets              *SecretCodec
	baseHostname         string
	maxCompatibilityDate string
	randomHostnameSuffix func() (string, error)
	kvOrgUsageCacheMu    sync.Mutex
	kvOrgUsageCache      map[string]orgKVUsageCacheEntry
	kvOrgUsageCacheTTL   time.Duration
	restoreCounts        sync.Map // map[databaseID + result]*atomic.Uint64
	runtimeDeploymentsMu sync.RWMutex
	runtimeDeployments   map[string][]ActiveDeployment
	runtimeVersions      map[string]uint64
}

type AssetResponse struct {
	Body        []byte
	ContentType string
	StatusCode  int
}

func NewService(store Repository, writer ConfigWriter) *Service {
	return newService(store, writer, nil)
}

func NewServiceWithObjects(store Repository, writer ConfigWriter, objects ObjectStore) *Service {
	return newService(store, writer, objects)
}

func NewServiceWithConsole(store Repository, writer ConfigWriter, objects ObjectStore, output WorkerOutputReader, traffic WorkerTrafficReader) *Service {
	service := newService(store, writer, objects)
	service.output = output
	service.traffic = traffic
	if reader, ok := traffic.(DatabaseMetricsTimeseriesReader); ok {
		service.dbTimeseries = reader
	}
	if reader, ok := traffic.(KVNamespaceMetricsTimeseriesReader); ok {
		service.kvTimeseries = reader
	}
	if reader, ok := traffic.(ObjectStorageBucketMetricsTimeseriesReader); ok {
		service.objectTimeseries = reader
	}
	if reader, ok := traffic.(WorkerMetricsTimeseriesReader); ok {
		service.workerTimeseries = reader
	}
	return service
}

func newService(store Repository, writer ConfigWriter, objects ObjectStore) *Service {
	return &Service{
		store:                store,
		telemetry:            telemetryState{policy: TelemetryPolicy{Enabled: true, SamplePercent: 100}},
		outputQueries:        make(chan struct{}, 16),
		writer:               writer,
		objects:              objects,
		maxCompatibilityDate: time.Now().UTC().Format("2006-01-02"),
		randomHostnameSuffix: randomHostnameSuffix,
		kvOrgUsageCache:      make(map[string]orgKVUsageCacheEntry),
		kvOrgUsageCacheTTL:   time.Second,
		runtimeDeployments:   make(map[string][]ActiveDeployment),
		runtimeVersions:      make(map[string]uint64),
	}
}

// SetMaxCompatibilityDate configures the newest date accepted by the runtime.
func (s *Service) SetMaxCompatibilityDate(date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("invalid maximum compatibility date %q: %w", date, err)
	}
	s.maxCompatibilityDate = date
	return nil
}

type orgKVUsageCacheEntry struct {
	size      int64
	expiresAt time.Time
}

func (s *Service) SetBaseHostname(hostname string) error {
	normalized, err := normalizeBaseHostname(hostname)
	if err != nil {
		return err
	}
	s.baseHostname = normalized
	return nil
}

func (s *Service) SetSecretCodec(codec *SecretCodec) {
	s.secrets = codec
}

func (s *Service) SetDBExecutor(db DBExecutor) {
	s.db = db
}

func (s *Service) RepositoryPoolStats() (RepositoryPoolStats, bool) {
	reader, ok := s.store.(RepositoryPoolStatsReader)
	if !ok {
		return RepositoryPoolStats{}, false
	}
	return reader.PoolStats(), true
}

func (s *Service) CreateApp(input CreateAppInput) (App, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.OrgID = strings.TrimSpace(input.OrgID)
	input.Hostname = strings.TrimSpace(input.Hostname)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.OAuthClientID = strings.TrimSpace(input.OAuthClientID)
	input.CreatedBy = strings.TrimSpace(input.CreatedBy)
	if input.Name == "" {
		return App{}, errors.New("name is required")
	}
	auth, err := normalizeAuthConfig(input.Auth)
	if err != nil {
		return App{}, err
	}
	if err := s.enforceOrgLimit(input.OrgID, "worker"); err != nil {
		return App{}, err
	}
	generated := input.Hostname == ""
	if !generated {
		hostname, err := normalizeHostname(input.Hostname)
		if err != nil {
			return App{}, err
		}
		input.Hostname = hostname
	}
	attempts := 1
	if generated {
		if s.baseHostname == "" {
			return App{}, errors.New("hostname is required when base hostname is not configured")
		}
		attempts = 5
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		hostname := input.Hostname
		if generated {
			var err error
			hostname, err = s.generatedHostname(input.Name, input.OrgID, attempt)
			if err != nil {
				return App{}, err
			}
		}
		appID, err := randomToken()
		if err != nil {
			return App{}, err
		}
		runtimeToken, err := randomToken()
		if err != nil {
			return App{}, err
		}
		app := App{ID: appID, OrgID: input.OrgID, Name: input.Name, Hostname: hostname, Auth: auth, ExternalID: input.ExternalID, OAuthClientID: input.OAuthClientID, CreatedBy: input.CreatedBy, RuntimeToken: runtimeToken, CreatedAt: time.Now().UTC()}
		if err := s.store.CreateApp(app); err != nil {
			if generated && errors.Is(err, ErrAppExists) {
				lastErr = err
				continue
			}
			return App{}, err
		}
		return app, nil
	}
	if lastErr != nil {
		return App{}, errors.New("could not generate unique hostname")
	}
	return App{}, errors.New("could not create app")
}

func (s *Service) CreateKVNamespace(input CreateKVNamespaceInput) (KVNamespace, error) {
	input.OrgID = strings.TrimSpace(input.OrgID)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.OAuthClientID = strings.TrimSpace(input.OAuthClientID)
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return KVNamespace{}, errors.New("name is required")
	}
	if err := s.enforceOrgLimit(input.OrgID, "kv namespace"); err != nil {
		return KVNamespace{}, err
	}
	namespaceID, err := randomToken()
	if err != nil {
		return KVNamespace{}, err
	}
	namespace := KVNamespace{ID: namespaceID, OrgID: input.OrgID, Name: name, ExternalID: input.ExternalID, OAuthClientID: input.OAuthClientID, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateKVNamespace(namespace); err != nil {
		return KVNamespace{}, err
	}
	return namespace, nil
}

func (s *Service) ListKVNamespaces() ([]KVNamespace, error) {
	return s.store.ListKVNamespaces()
}

func (s *Service) ListKVNamespacesForOrg(orgID string) ([]KVNamespace, error) {
	return s.store.ListKVNamespacesByOrg(strings.TrimSpace(orgID))
}

func (s *Service) KVNamespaceMetricsListForOrg(orgID string) ([]KVNamespaceMetricsItem, error) {
	namespaces, err := s.ListKVNamespacesForOrg(orgID)
	if err != nil {
		return nil, err
	}
	items := make([]KVNamespaceMetricsItem, 0, len(namespaces))
	for _, namespace := range namespaces {
		metrics, err := s.KVNamespaceMetrics(namespace.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, KVNamespaceMetricsItem{NamespaceID: namespace.ID, KVNamespaceMetrics: metrics})
	}
	return items, nil
}

func (s *Service) CreateDatabase(input CreateDatabaseInput) (Database, error) {
	input.OrgID = strings.TrimSpace(input.OrgID)
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Database{}, errors.New("name is required")
	}
	databaseID, err := randomToken()
	if err != nil {
		return Database{}, err
	}
	database := Database{ID: databaseID, OrgID: input.OrgID, Name: name, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateDatabase(database); err != nil {
		return Database{}, err
	}
	if s.db != nil {
		_ = s.db.RestoreMissing(database.ID)
	}
	return database, nil
}

func (s *Service) ListDatabases() ([]Database, error) {
	return s.store.ListDatabases()
}

func (s *Service) ListDatabasesForOrg(orgID string) ([]Database, error) {
	return s.store.ListDatabasesByOrg(strings.TrimSpace(orgID))
}

func (s *Service) DatabaseMetricsListForOrg(orgID string) ([]DatabaseMetricsItem, error) {
	databases, err := s.ListDatabasesForOrg(orgID)
	if err != nil {
		return nil, err
	}
	items := make([]DatabaseMetricsItem, 0, len(databases))
	for _, database := range databases {
		metrics, err := s.DatabaseMetrics(database.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, DatabaseMetricsItem{DatabaseID: database.ID, DatabaseMetrics: metrics})
	}
	return items, nil
}

func (s *Service) CreateObjectStorageBucket(input CreateObjectStorageBucketInput) (ObjectStorageBucket, error) {
	input.OrgID = strings.TrimSpace(input.OrgID)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.OAuthClientID = strings.TrimSpace(input.OAuthClientID)
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ObjectStorageBucket{}, errors.New("name is required")
	}
	if err := s.enforceOrgLimit(input.OrgID, "object storage bucket"); err != nil {
		return ObjectStorageBucket{}, err
	}
	bucketID, err := randomToken()
	if err != nil {
		return ObjectStorageBucket{}, err
	}
	bucket := ObjectStorageBucket{ID: bucketID, OrgID: input.OrgID, Name: name, ExternalID: input.ExternalID, OAuthClientID: input.OAuthClientID, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateObjectStorageBucket(bucket); err != nil {
		return ObjectStorageBucket{}, err
	}
	return bucket, nil
}

func (s *Service) ListObjectStorageBuckets() ([]ObjectStorageBucket, error) {
	return s.store.ListObjectStorageBuckets()
}

func (s *Service) ListObjectStorageBucketsForOrg(orgID string) ([]ObjectStorageBucket, error) {
	return s.store.ListObjectStorageBucketsByOrg(strings.TrimSpace(orgID))
}

func (s *Service) ObjectStorageBucketMetricsListForOrg(orgID string) ([]ObjectStorageBucketMetricsItem, error) {
	buckets, err := s.ListObjectStorageBucketsForOrg(orgID)
	if err != nil {
		return nil, err
	}
	items := make([]ObjectStorageBucketMetricsItem, 0, len(buckets))
	for _, bucket := range buckets {
		metrics, err := s.ObjectStorageBucketMetrics(bucket.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, ObjectStorageBucketMetricsItem{BucketID: bucket.ID, ObjectStorageBucketMetrics: metrics})
	}
	return items, nil
}

func (s *Service) enforceOrgLimit(orgID, resource string) error {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil
	}
	org, err := s.store.GetOrganization(orgID)
	if err != nil {
		return err
	}
	limits := OrgLimitsForLevel(org.UsageLevel)
	var limit *int
	var count int
	switch resource {
	case "worker":
		limit = limits.Workers
		count, err = s.store.CountAppsByOrg(orgID)
	case "kv namespace":
		limit = limits.KVNamespaces
		count, err = s.store.CountKVNamespacesByOrg(orgID)
	case "object storage bucket":
		limit = limits.ObjectStorageBuckets
		count, err = s.store.CountObjectStorageBucketsByOrg(orgID)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	if limit != nil && count >= *limit {
		return usageLimitError(org.UsageLevel, resource, *limit)
	}
	return nil
}

func (s *Service) GetObjectStorageBucket(bucketID string) (ObjectStorageBucket, error) {
	bucketID = strings.TrimSpace(bucketID)
	if bucketID == "" {
		return ObjectStorageBucket{}, ErrObjectStorageBucketNotFound
	}
	return s.store.GetObjectStorageBucket(bucketID)
}

func (s *Service) UpdateObjectStorageBucket(bucketID string, input UpdateObjectStorageBucketInput) (ObjectStorageBucket, error) {
	bucketID = strings.TrimSpace(bucketID)
	if bucketID == "" {
		return ObjectStorageBucket{}, ErrObjectStorageBucketNotFound
	}
	bucket, err := s.store.GetObjectStorageBucket(bucketID)
	if err != nil {
		return ObjectStorageBucket{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ObjectStorageBucket{}, errors.New("name is required")
	}
	bucket.Name = name
	if err := s.store.UpdateObjectStorageBucket(bucket); err != nil {
		return ObjectStorageBucket{}, err
	}
	return bucket, nil
}

func (s *Service) GetObjectStorageBucketForOrg(orgID, bucketID string) (ObjectStorageBucket, error) {
	bucket, err := s.GetObjectStorageBucket(bucketID)
	if err != nil {
		return ObjectStorageBucket{}, err
	}
	if strings.TrimSpace(orgID) != "" && bucket.OrgID != strings.TrimSpace(orgID) {
		return ObjectStorageBucket{}, ErrObjectStorageBucketNotFound
	}
	return bucket, nil
}

func (s *Service) DeleteObjectStorageBucket(bucketID string) error {
	bucketID = strings.TrimSpace(bucketID)
	if bucketID == "" {
		return ErrObjectStorageBucketNotFound
	}
	return s.store.DeleteObjectStorageBucket(bucketID)
}

func (s *Service) GetKVNamespace(namespaceID string) (KVNamespace, error) {
	namespaceID = strings.TrimSpace(namespaceID)
	if namespaceID == "" {
		return KVNamespace{}, ErrKVNamespaceNotFound
	}
	return s.store.GetKVNamespace(namespaceID)
}

func (s *Service) GetKVNamespaceForOrg(orgID, namespaceID string) (KVNamespace, error) {
	namespace, err := s.GetKVNamespace(namespaceID)
	if err != nil {
		return KVNamespace{}, err
	}
	if strings.TrimSpace(orgID) != "" && namespace.OrgID != strings.TrimSpace(orgID) {
		return KVNamespace{}, ErrKVNamespaceNotFound
	}
	return namespace, nil
}

func (s *Service) GetDatabase(databaseID string) (Database, error) {
	databaseID = strings.TrimSpace(databaseID)
	if databaseID == "" {
		return Database{}, ErrDatabaseNotFound
	}
	return s.store.GetDatabase(databaseID)
}

func (s *Service) GetDatabaseForOrg(orgID, databaseID string) (Database, error) {
	database, err := s.GetDatabase(databaseID)
	if err != nil {
		return Database{}, err
	}
	if strings.TrimSpace(orgID) != "" && database.OrgID != strings.TrimSpace(orgID) {
		return Database{}, ErrDatabaseNotFound
	}
	return database, nil
}

func (s *Service) DeleteDatabase(databaseID string) error {
	databaseID = strings.TrimSpace(databaseID)
	if databaseID == "" {
		return ErrDatabaseNotFound
	}
	if err := s.store.DeleteDatabase(databaseID); err != nil {
		return err
	}
	if s.db != nil {
		return s.db.Delete(databaseID)
	}
	return nil
}

func (s *Service) DeleteDatabaseForOrg(orgID, databaseID string) error {
	if _, err := s.GetDatabaseForOrg(orgID, databaseID); err != nil {
		return err
	}
	return s.DeleteDatabase(databaseID)
}

func (s *Service) UpdateKVNamespace(namespaceID string, input UpdateKVNamespaceInput) (KVNamespace, error) {
	namespaceID = strings.TrimSpace(namespaceID)
	if namespaceID == "" {
		return KVNamespace{}, ErrKVNamespaceNotFound
	}
	namespace, err := s.store.GetKVNamespace(namespaceID)
	if err != nil {
		return KVNamespace{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return KVNamespace{}, errors.New("name is required")
	}
	namespace.Name = name
	if err := s.store.UpdateKVNamespace(namespace); err != nil {
		return KVNamespace{}, err
	}
	return namespace, nil
}

func (s *Service) DeleteKVNamespace(namespaceID string) error {
	namespaceID = strings.TrimSpace(namespaceID)
	if namespaceID == "" {
		return ErrKVNamespaceNotFound
	}
	return s.store.DeleteKVNamespace(namespaceID)
}

func (s *Service) UpdateKVNamespaceForOrg(orgID, namespaceID string, input UpdateKVNamespaceInput) (KVNamespace, error) {
	namespace, err := s.GetKVNamespaceForOrg(orgID, namespaceID)
	if err != nil {
		return KVNamespace{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return KVNamespace{}, errors.New("name is required")
	}
	namespace.Name = name
	if err := s.store.UpdateKVNamespace(namespace); err != nil {
		return KVNamespace{}, err
	}
	return namespace, nil
}

func (s *Service) DeleteKVNamespaceForOrg(orgID, namespaceID string) error {
	if _, err := s.GetKVNamespaceForOrg(orgID, namespaceID); err != nil {
		return err
	}
	return s.DeleteKVNamespace(namespaceID)
}

func (s *Service) UpdateApp(appID string, input UpdateAppInput) (App, error) {
	if _, err := s.store.GetApp(appID); err != nil {
		return App{}, err
	}
	return s.updateApp(appID, input)
}

func (s *Service) updateApp(appID string, input UpdateAppInput) (App, error) {
	current, err := s.store.GetApp(appID)
	if err != nil {
		return App{}, err
	}
	if current.IsPrivate() && input.Hostname != nil {
		return App{}, fmt.Errorf("%w: private scripts cannot have a hostname", ErrInvalidAppUpdate)
	}
	if input.Hostname != nil {
		hostname, err := normalizeHostname(*input.Hostname)
		if err != nil {
			return App{}, fmt.Errorf("%w: %v", ErrInvalidAppUpdate, err)
		}
		input.Hostname = &hostname
	}
	if input.Auth != nil {
		auth, err := normalizeAuthConfig(*input.Auth)
		if err != nil {
			return App{}, fmt.Errorf("%w: %v", ErrInvalidAppUpdate, err)
		}
		input.Auth = &auth
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return App{}, err
	}
	defer release()
	app, err := s.store.PatchApp(appID, input)
	if err != nil {
		if errors.Is(err, ErrAppExists) {
			return App{}, fmt.Errorf("hostname is already assigned: %w", err)
		}
		return App{}, err
	}
	// Even an unchanged hostname must refresh routing to recover a prior failure.
	s.invalidateRuntimeDeployments(appID)
	active, err := s.activeDeployments()
	if err == nil {
		err = s.writer.Write(active)
	}
	if err != nil {
		return App{}, fmt.Errorf("worker update saved but routing refresh failed; GET the worker and retry PATCH: %w", err)
	}
	return app, nil
}

func (s *Service) ListSecrets(appID string) ([]Secret, error) {
	if _, err := s.store.GetApp(appID); err != nil {
		return nil, err
	}
	if artifact, managed, err := s.managedLatestArtifact(appID); err != nil {
		return nil, err
	} else if managed {
		result := []Secret{}
		for _, secret := range artifact.Secrets {
			created, updated := secret.CreatedAt, secret.UpdatedAt
			if created.IsZero() {
				created = artifact.Deployment.CreatedAt
			}
			if updated.IsZero() {
				updated = artifact.Deployment.CreatedAt
			}
			result = append(result, Secret{Name: secret.Name, CreatedAt: created, UpdatedAt: updated})
		}
		return result, nil
	}
	records, err := s.store.ListSecrets(appID)
	if err != nil {
		return nil, err
	}
	secrets := make([]Secret, 0, len(records))
	for _, record := range records {
		secrets = append(secrets, record.Secret)
	}
	return secrets, nil
}

func (s *Service) ListSecretsForOrg(orgID, appID string) ([]Secret, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return nil, err
	}
	return s.ListSecrets(appID)
}

func (s *Service) PutSecret(appID, name, value string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return err
	}
	defer release()
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("secret name is required")
	}
	if s.secrets == nil {
		return errors.New("NANOFLARE_SECRET_KEY is required for secret operations")
	}
	if _, _, err := s.worker(appID); err != nil {
		return err
	}
	if managed, err := s.mutateManagedSecretLocked(appID, name, &value); managed || err != nil {
		return err
	}
	records, err := s.store.ListSecrets(appID)
	if err != nil {
		return err
	}
	secretValues := make(map[string]string, len(records)+1)
	var createdAt time.Time
	for _, record := range records {
		secretValues[record.Name] = ""
		if record.Name == name {
			createdAt = record.CreatedAt
		}
	}
	secretValues[name] = value
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	for _, item := range activeForAppDeployments(active, appID) {
		if err := validateDispatchNameCollisions(item.Deployment.DispatchNamespaces, item.Deployment.Vars, secretValues); err != nil {
			return err
		}
		if err := validateBindingCollisions(item.Deployment.Vars, secretValues, item.Deployment.KVNamespaces, item.Deployment.Databases, item.Deployment.ObjectStorageBuckets, item.Deployment.Services, item.Deployment.AssetConfig); err != nil {
			return err
		}
	}
	if err := s.pinActiveArtifacts(appID); err != nil {
		return err
	}
	nonce, ciphertext, err := s.secrets.Encrypt(value)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if createdAt.IsZero() {
		createdAt = now
	}
	if err := s.store.PutSecret(appID, SecretRecord{
		Secret: Secret{Name: name, CreatedAt: createdAt, UpdatedAt: now},
		Nonce:  nonce, Ciphertext: ciphertext,
	}); err != nil {
		return err
	}
	return s.rolloutSecretsIfActive(appID)
}

func (s *Service) PutSecretForOrg(orgID, appID, name, value string) error {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return err
	}
	return s.PutSecret(appID, name, value)
}

func (s *Service) DeleteSecret(appID, name string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return err
	}
	defer release()
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("secret name is required")
	}
	if _, _, err := s.worker(appID); err != nil {
		return err
	}
	if managed, err := s.mutateManagedSecretLocked(appID, name, nil); managed || err != nil {
		return err
	}
	if err := s.pinActiveArtifacts(appID); err != nil {
		return err
	}
	if err := s.store.DeleteSecret(appID, name); err != nil {
		return err
	}
	return s.rolloutSecretsIfActive(appID)
}

func (s *Service) DeleteSecretForOrg(orgID, appID, name string) error {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return err
	}
	return s.DeleteSecret(appID, name)
}

func (s *Service) ListApps() ([]App, error) {
	return s.store.ListApps()
}

func (s *Service) ListOrganizations() ([]Organization, error) {
	return s.store.ListOrganizations()
}

func (s *Service) UserCount() (int, error) {
	return s.store.UserCount()
}

func (s *Service) ListOrganizationMembers(orgID string) ([]OrganizationMembership, error) {
	return s.store.ListOrganizationMembers(strings.TrimSpace(orgID))
}

func (s *Service) ListAppsForOrg(orgID string) ([]App, error) {
	return s.store.ListAppsByOrg(strings.TrimSpace(orgID))
}

func (s *Service) UpdateAppForOrg(orgID, appID string, input UpdateAppInput) (App, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return App{}, err
	}
	return s.updateApp(appID, input)
}

func (s *Service) DeleteAppForOrg(orgID, appID string) error {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return err
	}
	return s.DeleteApp(appID)
}

func (s *Service) UpdateObjectStorageBucketForOrg(orgID, bucketID string, input UpdateObjectStorageBucketInput) (ObjectStorageBucket, error) {
	bucket, err := s.GetObjectStorageBucketForOrg(orgID, bucketID)
	if err != nil {
		return ObjectStorageBucket{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ObjectStorageBucket{}, errors.New("name is required")
	}
	bucket.Name = name
	if err := s.store.UpdateObjectStorageBucket(bucket); err != nil {
		return ObjectStorageBucket{}, err
	}
	return bucket, nil
}

func (s *Service) DeleteObjectStorageBucketForOrg(orgID, bucketID string) error {
	if _, err := s.GetObjectStorageBucketForOrg(orgID, bucketID); err != nil {
		return err
	}
	return s.DeleteObjectStorageBucket(bucketID)
}

func (s *Service) DeleteApp(appID string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return err
	}
	defer release()
	records, err := s.store.ListDeployments()
	if err != nil {
		return err
	}
	apps, err := s.store.ListApps()
	if err != nil {
		return err
	}
	exists := false
	for _, app := range apps {
		if app.ID == appID {
			exists = true
			break
		}
	}
	if !exists {
		return ErrAppNotFound
	}
	if err := s.store.DeleteApp(appID); err != nil {
		return err
	}
	for _, record := range records {
		if record.App.ID != appID {
			continue
		}
		s.cleanupDeploymentObject(record.Deployment)
		s.cleanupDeploymentAssets(record.Deployment)
	}
	s.invalidateRuntimeDeployments(appID)
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	if err := s.writer.Write(active); err != nil {
		return fmt.Errorf("write generated config: %w", err)
	}
	return nil
}

func (s *Service) ActiveDeployments() ([]ActiveDeployment, error) {
	return s.activeDeployments()
}

func (s *Service) WorkerDetail(appID string) (WorkerDetail, error) {
	app, err := s.store.GetApp(appID)
	if err != nil {
		return WorkerDetail{}, err
	}
	deployments, err := s.store.ActiveDeploymentsForApp(appID)
	if err != nil {
		return WorkerDetail{}, err
	}
	var active *ActiveDeployment
	if len(deployments) > 0 {
		active = &deployments[0]
		if active.Deployment.BundleSize == 0 {
			active.Deployment.BundleSize = bundleSize(active.Deployment.Files)
		}
	}

	detail := WorkerDetail{App: app}
	secrets, err := s.ListSecrets(appID)
	if err != nil {
		return WorkerDetail{}, err
	}
	detail.Secrets = secrets
	if active == nil {
		return detail, nil
	}
	detail.Deployment = &WorkerDeployment{
		ID:                   active.Deployment.ID,
		CommitHash:           active.Deployment.CommitHash,
		CommitMessage:        active.Deployment.CommitMessage,
		CreatedBy:            active.Deployment.CreatedBy,
		Entrypoint:           active.Deployment.Entrypoint,
		Format:               active.Deployment.Format,
		BundleSize:           active.Deployment.BundleSize,
		AssetCount:           len(active.Deployment.Assets),
		CompatibilityDate:    active.Deployment.CompatibilityDate,
		CompatibilityFlags:   append([]string(nil), active.Deployment.CompatibilityFlags...),
		DNS:                  active.Deployment.DNS,
		Triggers:             active.Deployment.Triggers,
		Vars:                 cloneVars(active.Deployment.Vars),
		KVNamespaces:         append([]KVBinding(nil), active.Deployment.KVNamespaces...),
		Databases:            append([]DatabaseBinding(nil), active.Deployment.Databases...),
		ObjectStorageBuckets: append([]ObjectStorageBucketBinding(nil), active.Deployment.ObjectStorageBuckets...),
		AssetConfig:          active.Deployment.AssetConfig,
		DispatchNamespaces:   append([]DispatchBinding(nil), active.Deployment.DispatchNamespaces...),
		Bindings:             s.deploymentBindings(active.Deployment),
		Port:                 active.Deployment.Port,
		TrafficPercent:       active.TrafficPercent,
		CreatedAt:            active.Deployment.CreatedAt,
	}
	return detail, nil
}

func (s *Service) WorkerDetailForOrg(orgID, appID string) (WorkerDetail, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return WorkerDetail{}, err
	}
	return s.WorkerDetail(appID)
}

func (s *Service) WorkerDeployments(appID string) ([]ConsoleDeployment, error) {
	if _, _, err := s.worker(appID); err != nil {
		return nil, err
	}
	records, err := s.store.ListDeployments()
	if err != nil {
		return nil, err
	}
	deployments := make([]ConsoleDeployment, 0, len(records))
	for _, record := range records {
		if record.App.ID != appID {
			continue
		}
		state := "inactive"
		if record.Active {
			state = "active"
		}
		deployments = append(deployments, ConsoleDeployment{
			ID:                 record.Deployment.ID,
			AppID:              record.App.ID,
			AppName:            record.App.Name,
			Hostname:           record.App.Hostname,
			CommitHash:         record.Deployment.CommitHash,
			CommitMessage:      record.Deployment.CommitMessage,
			CreatedBy:          record.Deployment.CreatedBy,
			Entrypoint:         record.Deployment.Entrypoint,
			Format:             record.Deployment.Format,
			BundleSize:         record.Deployment.BundleSize,
			AssetCount:         len(record.Deployment.Assets),
			CompatibilityDate:  record.Deployment.CompatibilityDate,
			CompatibilityFlags: append([]string(nil), record.Deployment.CompatibilityFlags...),
			DNS:                record.Deployment.DNS,
			Triggers:           record.Deployment.Triggers,
			State:              state,
			TrafficPercent:     record.TrafficPercent,
			CreatedAt:          record.Deployment.CreatedAt,
		})
	}
	return deployments, nil
}

func (s *Service) SetDeploymentTrafficForOrg(orgID, appID string, traffic []DeploymentTraffic) ([]ConsoleDeployment, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return nil, err
	}
	return s.SetDeploymentTraffic(appID, traffic)
}

func (s *Service) SetDeploymentTraffic(appID string, traffic []DeploymentTraffic) ([]ConsoleDeployment, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := validateDeploymentTraffic(traffic); err != nil {
		return nil, err
	}
	for i := range traffic {
		traffic[i].ID = strings.TrimSpace(traffic[i].ID)
	}
	if _, _, err := s.worker(appID); err != nil {
		return nil, err
	}
	previous, err := s.activeDeployments()
	if err != nil {
		return nil, err
	}
	previousTraffic := activeTrafficForApp(previous, appID)
	if err := s.store.SetActiveTraffic(appID, traffic); err != nil {
		return nil, err
	}
	s.invalidateRuntimeDeployments(appID)
	active, err := s.activeDeployments()
	if err != nil {
		_ = s.store.SetActiveTraffic(appID, previousTraffic)
		s.invalidateRuntimeDeployments(appID)
		return nil, err
	}
	if err := s.writer.Write(active); err != nil {
		_ = s.store.SetActiveTraffic(appID, previousTraffic)
		s.invalidateRuntimeDeployments(appID)
		return nil, fmt.Errorf("write generated config: %w", err)
	}
	return s.WorkerDeployments(appID)
}

func (s *Service) WorkerDeploymentsForOrg(orgID, appID string) ([]ConsoleDeployment, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return nil, err
	}
	return s.WorkerDeployments(appID)
}

func (s *Service) WorkerFiles(appID string) ([]WorkerFile, error) {
	_, active, err := s.worker(appID)
	if err != nil {
		return nil, err
	}
	if active == nil {
		return []WorkerFile{}, nil
	}
	return append([]WorkerFile(nil), active.Deployment.Files...), nil
}

func (s *Service) WorkerFilesForOrg(orgID, appID string) ([]WorkerFile, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return nil, err
	}
	return s.WorkerFiles(appID)
}

func (s *Service) WorkerOutput(appID string) ([]WorkerOutputLine, error) {
	if _, _, err := s.worker(appID); err != nil {
		return nil, err
	}
	if s.output == nil {
		return []WorkerOutputLine{}, nil
	}
	active, err := s.activeDeployments()
	if err != nil {
		return nil, err
	}
	activeIDs := make(map[string]bool)
	for _, item := range activeForAppDeployments(active, appID) {
		activeIDs[item.Deployment.ID] = true
	}
	lines := s.output.Output(appID)
	filtered := make([]WorkerOutputLine, 0, len(lines))
	for _, line := range lines {
		if activeIDs[line.DeploymentID] {
			filtered = append(filtered, line)
		}
	}
	return filtered, nil
}

func (s *Service) WorkerOutputForOrg(orgID, appID string) ([]WorkerOutputLine, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return nil, err
	}
	return s.WorkerOutput(appID)
}

func (s *Service) WorkerOutputQueryForOrg(ctx context.Context, orgID, appID string, query WorkerOutputQuery) ([]WorkerOutputLine, error) {
	return s.queryWorkerOutput(ctx, orgID, appID, query)
}

func (s *Service) WorkerTraffic(appID string) (WorkerTraffic, error) {
	_, active, err := s.worker(appID)
	if err != nil {
		return WorkerTraffic{}, err
	}
	if s.traffic == nil {
		traffic := WorkerTraffic{}
		if active != nil {
			traffic.BundleSize = active.Deployment.BundleSize
		}
		return traffic, nil
	}
	traffic, err := s.traffic.Traffic(appID)
	if err != nil {
		return WorkerTraffic{}, err
	}
	if active != nil {
		traffic.BundleSize = active.Deployment.BundleSize
	}
	return traffic, nil
}

func (s *Service) WorkerTrafficForOrg(orgID, appID string) (WorkerTraffic, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return WorkerTraffic{}, err
	}
	return s.WorkerTraffic(appID)
}

func (s *Service) worker(appID string) (App, *ActiveDeployment, error) {
	apps, err := s.store.ListApps()
	if err != nil {
		return App{}, nil, err
	}
	var app *App
	for i := range apps {
		if apps[i].ID == appID {
			app = &apps[i]
			break
		}
	}
	if app == nil {
		return App{}, nil, ErrAppNotFound
	}
	active, err := s.activeDeployments()
	if err != nil {
		return App{}, nil, err
	}
	for i := range active {
		if active[i].App.ID == appID {
			return *app, &active[i], nil
		}
	}
	return *app, nil, nil
}

func (s *Service) appForOrg(orgID, appID string) (App, error) {
	apps, err := s.store.ListAppsByOrg(strings.TrimSpace(orgID))
	if err != nil {
		return App{}, err
	}
	for _, app := range apps {
		if app.ID == appID {
			return app, nil
		}
	}
	return App{}, ErrAppNotFound
}

func (s *Service) Deploy(appID string, input DeployInput) (Deployment, error) {
	return s.deployPublication(appID, input)
}
func (s *Service) buildAndPublishDeployment(appID string, input DeployInput, fingerprint string, managed *managedPublication) (result Deployment, retErr error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return Deployment{}, err
	}
	if err := s.validateDispatchBindings(app, input); err != nil {
		return Deployment{}, err
	}
	if err := s.checkNamespaceBindings(app, input); err != nil {
		return Deployment{}, err
	}
	files, entrypoint, err := deploymentFiles(input.Files, input.Entrypoint)
	if err != nil {
		return Deployment{}, err
	}
	assets, err := deploymentAssets(input.Assets, input.AssetConfig)
	if err != nil {
		return Deployment{}, err
	}
	assetConfig, err := normalizeAssetConfig(input.AssetConfig)
	if err != nil {
		return Deployment{}, err
	}
	kvNamespaces, err := s.normalizeKVNamespaces(input.KVNamespaces)
	if err != nil {
		return Deployment{}, err
	}
	databases, err := s.normalizeDatabases(input.Databases)
	if err != nil {
		return Deployment{}, err
	}
	objectStorageBuckets, err := s.normalizeObjectStorageBuckets(input.ObjectStorageBuckets)
	if err != nil {
		return Deployment{}, err
	}
	services, err := s.normalizeServiceBindings(appID, input.Services)
	if err != nil {
		return Deployment{}, err
	}
	vars, err := normalizeVars(input.Vars)
	if err != nil {
		return Deployment{}, err
	}
	triggers, err := NormalizeTriggers(input.Triggers)
	if err != nil {
		return Deployment{}, err
	}
	format, err := workerFormat(input.Format, len(files))
	if err != nil {
		return Deployment{}, err
	}
	compatibilityDate, err := time.Parse("2006-01-02", input.CompatibilityDate)
	if err != nil {
		return Deployment{}, errors.New("compatibility_date must use YYYY-MM-DD")
	}
	maxCompatibilityDate, err := time.Parse("2006-01-02", s.maxCompatibilityDate)
	if err != nil {
		return Deployment{}, fmt.Errorf("invalid server compatibility date: %w", err)
	}
	if compatibilityDate.After(maxCompatibilityDate) {
		log.Printf("warning: requested compatibility date %q is newer than this server supports; falling back to %q", input.CompatibilityDate, s.maxCompatibilityDate)
		input.CompatibilityDate = s.maxCompatibilityDate
	}
	compatibilityFlags := normalizeCompatibilityFlags(input.CompatibilityFlags)
	dns := DNSSelection{Profile: strings.TrimSpace(input.DNS.Profile)}
	deploymentID, err := randomToken()
	if err != nil {
		return Deployment{}, err
	}
	deployment := Deployment{
		ID:                   deploymentID,
		AppID:                appID,
		CommitHash:           strings.TrimSpace(input.CommitHash),
		CommitMessage:        strings.TrimSpace(input.CommitMessage),
		CreatedBy:            strings.TrimSpace(input.CreatedBy),
		Files:                files,
		Assets:               assets,
		Entrypoint:           entrypoint,
		Format:               format,
		CompatibilityDate:    input.CompatibilityDate,
		CompatibilityFlags:   compatibilityFlags,
		DNS:                  dns,
		Triggers:             triggers,
		Vars:                 vars,
		KVNamespaces:         kvNamespaces,
		Databases:            databases,
		ObjectStorageBuckets: objectStorageBuckets,
		Services:             services,
		DispatchNamespaces:   append([]DispatchBinding(nil), input.DispatchNamespaces...),
		AssetConfig:          assetConfig,
		BundleSize:           bundleSize(files),
		CreatedAt:            time.Now().UTC(),
	}
	if managed == nil {
		managed, err = s.nativeManagedPublication(app, deployment)
		if err != nil {
			return Deployment{}, err
		}
	}
	original, err := s.store.ActiveDeploymentsForApp(appID)
	if err != nil {
		return Deployment{}, err
	}
	publication := Publication{AppID: appID, Key: input.IdempotencyKey, Fingerprint: fingerprint, DeploymentID: deployment.ID, Stage: "building", PreviousTraffic: activeTrafficForApp(original, appID), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if managed != nil {
		publication.RequestIdentity = managed.RequestIdentity
		publication.ConfigurationOnly = managed.ConfigurationOnly
	}
	if err = s.store.SavePublication(publication); err != nil {
		return Deployment{}, err
	}
	defer func() {
		if retErr != nil {
			current, found, e := s.store.GetPublication(appID, input.IdempotencyKey)
			if e == nil && found && current.Stage == "building" {
				_ = s.savePublication(&current, "failed")
			}
		}
	}()
	if len(deployment.Assets) > 0 && s.objects == nil {
		return Deployment{}, errors.New("object storage is not configured")
	}
	if s.objects != nil {
		payload, err := json.Marshal(files)
		if err != nil {
			return Deployment{}, err
		}
		deployment.ObjectKey = deploymentBundleObjectPath(deploymentID)
		if _, err := s.objects.Put(appID, deployment.ObjectKey, "application/json", payload); err != nil {
			return Deployment{}, err
		}
		for i := range deployment.Assets {
			deployment.Assets[i].ObjectKey = deploymentAssetObjectPath(deploymentID, deployment.Assets[i].Path)
			if _, err := s.objects.Put(appID, deployment.Assets[i].ObjectKey, deployment.Assets[i].ContentType, deployment.Assets[i].Data); err != nil {
				s.cleanupDeploymentAssets(deployment)
				s.cleanupDeploymentObject(deployment)
				return Deployment{}, err
			}
			deployment.Assets[i].Data = nil
		}
	}
	// Check secrets under the publication guard so a concurrent configuration
	// rollout cannot introduce a binding collision.
	var secrets map[string]string
	if managed != nil {
		secrets = map[string]string{}
		for _, secret := range managed.Secrets {
			secrets[secret.Name] = ""
		}
	} else {
		secrets, err = s.resolvedSecretValues(appID)
	}
	if err == nil {
		err = validateBindingCollisions(vars, secrets, kvNamespaces, databases, objectStorageBuckets, services, input.AssetConfig)
	}
	if err != nil {
		s.cleanupDeploymentObject(deployment)
		s.cleanupDeploymentAssets(deployment)
		return Deployment{}, err
	}
	var artifact DeploymentArtifact
	if managed != nil {
		artifact, err = s.captureArtifact(ActiveDeployment{App: app, Deployment: deployment}, managed.Secrets)
		if err == nil {
			artifact.Management = &ArtifactManagement{ExpectedRevision: managed.ExpectedRevision, Metadata: managed.Metadata}
			err = s.store.PutDeploymentArtifact(artifact)
		}
	} else {
		artifact, err = s.freezeArtifact(ActiveDeployment{App: app, Deployment: deployment}, nil)
	}
	if err != nil {
		return Deployment{}, err
	}
	publication.Digest = artifact.Digest
	publication.Stage = "stored"
	if err = s.store.StoreDeployment(deployment); err != nil {
		return Deployment{}, err
	}
	if err = s.store.SavePublication(publication); err != nil {
		return Deployment{}, err
	}
	return s.publishStoredArtifact(&publication, artifact)
}

func (s *Service) DeployForOrg(orgID, appID string, input DeployInput) (Deployment, error) {
	if err := s.validateDeploymentForOrg(orgID, appID, input); err != nil {
		return Deployment{}, err
	}
	return s.Deploy(appID, input)
}
func (s *Service) validateDeploymentForOrg(orgID, appID string, input DeployInput) error {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return err
	}
	for _, binding := range input.KVNamespaces {
		if _, err := s.GetKVNamespaceForOrg(orgID, strings.TrimSpace(binding.ID)); err != nil {
			return err
		}
	}
	for _, binding := range input.Databases {
		if _, err := s.GetDatabaseForOrg(orgID, strings.TrimSpace(binding.DatabaseID)); err != nil {
			return err
		}
	}
	for _, binding := range input.ObjectStorageBuckets {
		if _, err := s.GetObjectStorageBucketForOrg(orgID, strings.TrimSpace(binding.BucketID)); err != nil {
			return err
		}
	}
	return nil
}

func workerFormat(format string, fileCount int) (string, error) {
	switch strings.TrimSpace(format) {
	case "":
		if fileCount == 1 {
			return "service-worker", nil
		}
		return "modules", nil
	case "modules", "service-worker":
		return format, nil
	default:
		return "", errors.New(`format must be "modules" or "service-worker"`)
	}
}

const maxWorkerBundleSize = 10 << 20

func deploymentFiles(files []WorkerFile, entrypoint string) ([]WorkerFile, string, error) {
	if len(files) == 0 {
		return nil, "", errors.New("files are required")
	}
	result := make([]WorkerFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	totalSize := 0
	for _, file := range files {
		file.Path = path.Clean(strings.TrimSpace(file.Path))
		if strings.HasPrefix(file.Path, "nanoflare-internal:") || strings.HasPrefix(file.Path, "__nanoflare_") {
			return nil, "", errors.New("worker file uses a reserved runtime module name")
		}
		if file.Path == "." || strings.HasPrefix(file.Path, "/") || strings.HasPrefix(file.Path, "../") {
			return nil, "", errors.New("file paths must be relative and remain inside the worker")
		}
		if seen[file.Path] {
			return nil, "", fmt.Errorf("duplicate worker file path %q", file.Path)
		}
		seen[file.Path] = true
		file.Name = path.Base(file.Path)
		file.Size = int64(len(file.Content))
		totalSize += len(file.Content)
		if totalSize > maxWorkerBundleSize {
			return nil, "", fmt.Errorf("worker files exceed %d byte limit", maxWorkerBundleSize)
		}
		result = append(result, file)
	}
	entrypoint = path.Clean(strings.TrimSpace(entrypoint))
	if entrypoint == "." {
		entrypoint = result[0].Path
	}
	if !seen[entrypoint] {
		return nil, "", errors.New("entrypoint must name a deployed worker file")
	}
	return result, entrypoint, nil
}

func deploymentAssets(files []AssetFile, config AssetConfig) ([]AssetFile, error) {
	if len(files) == 0 {
		return nil, nil
	}
	result := make([]AssetFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		file.Path = path.Clean(strings.TrimSpace(file.Path))
		if file.Path == "." || strings.HasPrefix(file.Path, "/") || strings.HasPrefix(file.Path, "../") {
			return nil, errors.New("asset paths must be relative and remain inside the asset directory")
		}
		if seen[file.Path] {
			return nil, fmt.Errorf("duplicate asset path %q", file.Path)
		}
		seen[file.Path] = true
		file.Size = int64(len(file.Data))
		if file.ContentType == "" {
			file.ContentType = detectAssetContentType(file.Path)
		}
		result = append(result, file)
	}
	if len(result) > 0 && strings.TrimSpace(config.HTMLHandling) == "" {
		config.HTMLHandling = "auto-trailing-slash"
	}
	return result, nil
}

func normalizeAssetConfig(config AssetConfig) (AssetConfig, error) {
	config.Binding = strings.TrimSpace(config.Binding)
	if config.Binding == "" {
		config.Binding = "ASSETS"
	}
	if config.HTMLHandling == "" {
		config.HTMLHandling = "auto-trailing-slash"
	}
	switch config.HTMLHandling {
	case "none", "auto-trailing-slash":
	default:
		return AssetConfig{}, errors.New(`asset_config.html_handling must be "none" or "auto-trailing-slash"`)
	}
	if config.NotFoundHandling == "" {
		config.NotFoundHandling = "404-page"
	}
	switch config.NotFoundHandling {
	case "none", "404-page", "single-page-application":
	default:
		return AssetConfig{}, errors.New(`asset_config.not_found_handling must be "none", "404-page", or "single-page-application"`)
	}
	if err := validateRunWorkerFirst(config.RunWorkerFirst); err != nil {
		return AssetConfig{}, err
	}
	return config, nil
}

func validateRunWorkerFirst(runWorkerFirst RunWorkerFirst) error {
	if runWorkerFirst.Always() {
		return nil
	}
	for _, route := range runWorkerFirst.Routes() {
		pattern := strings.TrimPrefix(route, "!")
		if !strings.HasPrefix(pattern, "/") || pattern == "/" || strings.Contains(strings.TrimSuffix(pattern, "*"), "*") {
			return fmt.Errorf("asset_config.run_worker_first route %q must be an absolute path with an optional trailing wildcard", route)
		}
	}
	return nil
}

func (s *Service) normalizeKVNamespaces(bindings []KVBinding) ([]KVBinding, error) {
	if len(bindings) == 0 {
		return nil, nil
	}
	normalized := make([]KVBinding, 0, len(bindings))
	seenBindings := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		binding.Binding = strings.TrimSpace(binding.Binding)
		binding.ID = strings.TrimSpace(binding.ID)
		binding.PreviewID = strings.TrimSpace(binding.PreviewID)
		if binding.Binding == "" {
			return nil, errors.New("kv_namespaces.binding is required")
		}
		if binding.ID == "" {
			return nil, errors.New("kv_namespaces.id is required")
		}
		if seenBindings[binding.Binding] {
			return nil, fmt.Errorf("kv_namespaces binding %q is duplicated", binding.Binding)
		}
		if _, err := s.store.GetKVNamespace(binding.ID); err != nil {
			return nil, err
		}
		seenBindings[binding.Binding] = true
		normalized = append(normalized, binding)
	}
	return normalized, nil
}

func (s *Service) normalizeDatabases(bindings []DatabaseBinding) ([]DatabaseBinding, error) {
	if len(bindings) == 0 {
		return nil, nil
	}
	normalized := make([]DatabaseBinding, 0, len(bindings))
	seenBindings := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		binding.Binding = strings.TrimSpace(binding.Binding)
		binding.DatabaseID = strings.TrimSpace(binding.DatabaseID)
		if binding.Binding == "" {
			return nil, errors.New("db.binding is required")
		}
		if binding.DatabaseID == "" {
			return nil, errors.New("db.database_id is required")
		}
		if seenBindings[binding.Binding] {
			return nil, fmt.Errorf("db binding %q is duplicated", binding.Binding)
		}
		if _, err := s.store.GetDatabase(binding.DatabaseID); err != nil {
			return nil, err
		}
		seenBindings[binding.Binding] = true
		normalized = append(normalized, binding)
	}
	return normalized, nil
}

func (s *Service) normalizeObjectStorageBuckets(bindings []ObjectStorageBucketBinding) ([]ObjectStorageBucketBinding, error) {
	if len(bindings) == 0 {
		return nil, nil
	}
	normalized := make([]ObjectStorageBucketBinding, 0, len(bindings))
	seenBindings := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		binding.Binding = strings.TrimSpace(binding.Binding)
		binding.BucketID = strings.TrimSpace(binding.BucketID)
		if binding.Binding == "" {
			return nil, errors.New("object_storage_buckets.binding is required")
		}
		if binding.BucketID == "" {
			return nil, errors.New("object_storage_buckets.bucket_id is required")
		}
		if seenBindings[binding.Binding] {
			return nil, fmt.Errorf("object_storage_buckets binding %q is duplicated", binding.Binding)
		}
		if _, err := s.store.GetObjectStorageBucket(binding.BucketID); err != nil {
			return nil, err
		}
		seenBindings[binding.Binding] = true
		normalized = append(normalized, binding)
	}
	return normalized, nil
}

func (s *Service) normalizeServiceBindings(appID string, bindings []ServiceBinding) ([]ServiceBinding, error) {
	if len(bindings) == 0 {
		return nil, nil
	}
	app, err := s.store.GetApp(appID)
	if err != nil {
		return nil, err
	}
	apps, err := s.store.ListApps()
	if err != nil {
		return nil, err
	}
	active, err := s.activeDeployments()
	if err != nil {
		return nil, err
	}
	out := make([]ServiceBinding, 0, len(bindings))
	seen := map[string]bool{}
	for _, b := range bindings {
		b.Binding = strings.TrimSpace(b.Binding)
		b.Service = strings.TrimSpace(b.Service)
		b.WorkerID = strings.TrimSpace(b.WorkerID)
		if b.Binding == "" || (b.Service == "" && b.WorkerID == "") || seen[b.Binding] {
			return nil, errors.New("service requires a unique binding and service name or worker_id")
		}
		var targets []App
		for _, candidate := range apps {
			if candidate.OrgID != app.OrgID {
				continue
			}
			if (b.WorkerID != "" && candidate.ID == b.WorkerID) || (b.WorkerID == "" && candidate.Platform == nil && candidate.Name == b.Service) {
				targets = append(targets, candidate)
			}
		}
		if len(targets) > 1 {
			return nil, ErrAmbiguousService
		}
		if len(targets) == 0 {
			return nil, errors.New("service binding targets unknown Worker in this organization")
		}
		target := targets[0]
		b.Execution = ""
		if target.IsDynamic() {
			if !app.IsDynamic() {
				return nil, errors.New("dynamic fetch services require a dynamic caller; platform hosts use dispatch_namespaces")
			}
			b.Execution = "dynamic"
		}
		if b.WorkerID != "" && b.Service != "" && target.Name != b.Service {
			return nil, errors.New("service and worker_id identify different Workers")
		}
		if !ServiceTargetAllowed(app, target) {
			return nil, ErrNamespaceGrantDenied
		}
		ready := false
		for _, d := range active {
			if d.App.ID == target.ID {
				ready = true
				break
			}
		}
		if !ready {
			return nil, errors.New("service target has no active deployment")
		}
		seen[b.Binding] = true
		out = append(out, b)
	}
	return out, nil
}

func normalizeVars(vars map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if len(vars) == 0 {
		return nil, nil
	}
	normalized := make(map[string]json.RawMessage, len(vars))
	for name, value := range vars {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("vars binding name is required")
		}
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 {
			return nil, fmt.Errorf("vars.%s must be valid JSON", name)
		}
		if !json.Valid(trimmed) {
			return nil, fmt.Errorf("vars.%s must be valid JSON", name)
		}
		if _, exists := normalized[name]; exists {
			return nil, fmt.Errorf("duplicate vars binding name %q after normalization", name)
		}
		normalized[name] = append(json.RawMessage(nil), trimmed...)
	}
	return normalized, nil
}

func validateBindingCollisions(vars map[string]json.RawMessage, secrets map[string]string, kvBindings []KVBinding, dbBindings []DatabaseBinding, objectBindings []ObjectStorageBucketBinding, serviceBindings []ServiceBinding, assetConfig AssetConfig) error {
	seen := make(map[string]string)
	add := func(name, kind string) error {
		if strings.HasPrefix(name, "__NANOFLARE_") {
			return fmt.Errorf("binding %q uses a reserved runtime name", name)
		}
		if existing, exists := seen[name]; exists {
			return fmt.Errorf("binding %q is defined by both %s and %s", name, existing, kind)
		}
		seen[name] = kind
		return nil
	}
	for name := range vars {
		if err := add(name, "vars"); err != nil {
			return err
		}
	}
	for name := range secrets {
		if err := add(name, "secrets"); err != nil {
			return err
		}
	}
	for _, binding := range kvBindings {
		if err := add(binding.Binding, "kv_namespaces"); err != nil {
			return err
		}
	}
	for _, binding := range dbBindings {
		if err := add(binding.Binding, "db"); err != nil {
			return err
		}
	}
	if err := add(deploymentAssetBindingName(assetConfig), "assets"); err != nil {
		return err
	}
	for _, binding := range objectBindings {
		if err := add(binding.Binding, "object_storage_buckets"); err != nil {
			return err
		}
	}
	for _, binding := range serviceBindings {
		if err := add(binding.Binding, "services"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deploymentBindings(deployment Deployment) []Binding {
	bindings := make([]Binding, 0, len(deployment.KVNamespaces)+len(deployment.Databases)+len(deployment.ObjectStorageBuckets)+len(deployment.Services)+1)
	for _, binding := range deployment.KVNamespaces {
		item := Binding{Kind: "kv", Binding: binding.Binding, NamespaceID: binding.ID}
		if namespace, err := s.store.GetKVNamespace(binding.ID); err == nil {
			item.NamespaceName = namespace.Name
		}
		bindings = append(bindings, item)
	}
	for _, binding := range deployment.Databases {
		item := Binding{Kind: "db", Binding: binding.Binding, DatabaseID: binding.DatabaseID}
		if database, err := s.store.GetDatabase(binding.DatabaseID); err == nil {
			item.DatabaseName = database.Name
		}
		bindings = append(bindings, item)
	}
	for _, binding := range deployment.Services {
		bindings = append(bindings, Binding{Kind: BindingKindService, Binding: binding.Binding, Service: binding.Service})
	}
	if len(deployment.Assets) > 0 {
		bindings = append(bindings, Binding{
			Kind:       "asset",
			Binding:    deploymentAssetBindingName(deployment.AssetConfig),
			AssetCount: len(deployment.Assets),
		})
	}
	for _, binding := range deployment.ObjectStorageBuckets {
		item := Binding{Kind: "object_storage_bucket", Binding: binding.Binding, BucketID: binding.BucketID}
		if bucket, err := s.store.GetObjectStorageBucket(binding.BucketID); err == nil {
			item.BucketName = bucket.Name
		}
		bindings = append(bindings, item)
	}
	return bindings
}

func deploymentAssetBindingName(config AssetConfig) string {
	if strings.TrimSpace(config.Binding) == "" {
		return "ASSETS"
	}
	return strings.TrimSpace(config.Binding)
}

func normalizeAuthConfig(config AuthConfig) (AuthConfig, error) {
	if len(config.ProtectedRoutes) == 0 {
		return AuthConfig{}, nil
	}
	routes := make([]string, 0, len(config.ProtectedRoutes))
	seen := make(map[string]bool, len(config.ProtectedRoutes))
	for _, route := range config.ProtectedRoutes {
		route = strings.TrimSpace(route)
		if err := validateProtectedRoute(route); err != nil {
			return AuthConfig{}, err
		}
		if seen[route] {
			continue
		}
		seen[route] = true
		routes = append(routes, route)
	}
	return AuthConfig{ProtectedRoutes: routes}, nil
}

func validateProtectedRoute(route string) error {
	if route == "" {
		return errors.New("auth.protected_routes cannot contain empty values")
	}
	if !strings.HasPrefix(route, "/") {
		return fmt.Errorf("auth.protected_routes route %q must be an absolute path", route)
	}
	if strings.Contains(strings.TrimSuffix(route, "*"), "*") {
		return fmt.Errorf("auth.protected_routes route %q must use at most one trailing wildcard", route)
	}
	if strings.HasSuffix(route, "*") && !strings.HasSuffix(route, "/*") {
		return fmt.Errorf("auth.protected_routes route %q wildcard must be written as /*", route)
	}
	return nil
}

func detectAssetContentType(assetPath string) string {
	if value := mime.TypeByExtension(strings.ToLower(path.Ext(assetPath))); value != "" {
		return value
	}
	return "application/octet-stream"
}

func activeTrafficForApp(active []ActiveDeployment, appID string) []DeploymentTraffic {
	var traffic []DeploymentTraffic
	for _, item := range active {
		if item.App.ID == appID && item.TrafficPercent > 0 {
			traffic = append(traffic, DeploymentTraffic{ID: item.Deployment.ID, TrafficPercent: item.TrafficPercent})
		}
	}
	return traffic
}

func activeForApp(active []ActiveDeployment, appID string) *ActiveDeployment {
	var best *ActiveDeployment
	for i := range active {
		if active[i].App.ID != appID {
			continue
		}
		if best == nil ||
			active[i].TrafficPercent > best.TrafficPercent ||
			(active[i].TrafficPercent == best.TrafficPercent && active[i].Deployment.CreatedAt.After(best.Deployment.CreatedAt)) {
			best = &active[i]
		}
	}
	return best
}

func activeForAppDeployments(active []ActiveDeployment, appID string) []ActiveDeployment {
	var deployments []ActiveDeployment
	for _, item := range active {
		if item.App.ID == appID && item.TrafficPercent > 0 {
			deployments = append(deployments, item)
		}
	}
	return deployments
}

func selectWeightedDeployment(active []ActiveDeployment) *ActiveDeployment {
	return selectWeightedDeploymentWithPreference(active, "")
}

func selectWeightedDeploymentWithPreference(active []ActiveDeployment, preferredDeploymentID string) *ActiveDeployment {
	if len(active) == 0 {
		return nil
	}
	if preferredDeploymentID != "" {
		for i := range active {
			if active[i].Deployment.ID == preferredDeploymentID && active[i].TrafficPercent > 0 {
				return &active[i]
			}
		}
	}
	total := 0
	for _, item := range active {
		total += item.TrafficPercent
	}
	if total <= 0 {
		return nil
	}
	target := cryptoRandomInt(total)
	for i := range active {
		target -= active[i].TrafficPercent
		if target < 0 {
			return &active[i]
		}
	}
	return &active[len(active)-1]
}

func cryptoRandomInt(max int) int {
	if max <= 1 {
		return 0
	}
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return int(time.Now().UnixNano() % int64(max))
	}
	var value uint64
	for _, item := range bytes {
		value = (value << 8) | uint64(item)
	}
	return int(value % uint64(max))
}

func validateDeploymentTraffic(traffic []DeploymentTraffic) error {
	if len(traffic) == 0 {
		return errors.New("deployments are required")
	}
	seen := make(map[string]bool, len(traffic))
	total := 0
	for _, item := range traffic {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return errors.New("deployment id is required")
		}
		if seen[id] {
			return errors.New("deployment ids must be unique")
		}
		seen[id] = true
		if item.TrafficPercent < 1 || item.TrafficPercent > 100 {
			return errors.New("traffic_percent must be between 1 and 100")
		}
		total += item.TrafficPercent
	}
	if total != 100 {
		return errors.New("traffic_percent values must sum to 100")
	}
	return nil
}

func normalizeCompatibilityFlags(flags []string) []string {
	normalized := make([]string, 0, len(flags))
	for _, flag := range flags {
		flag = strings.TrimSpace(flag)
		if flag != "" {
			normalized = append(normalized, flag)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func cloneVars(vars map[string]json.RawMessage) map[string]json.RawMessage {
	if len(vars) == 0 {
		return nil
	}
	cloned := make(map[string]json.RawMessage, len(vars))
	for name, value := range vars {
		cloned[name] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func (s *Service) activeDeployments() ([]ActiveDeployment, error) {
	active, err := s.store.ActiveDeployments()
	if err != nil {
		return nil, err
	}
	for i := range active {
		if err := s.hydrateArtifact(&active[i]); err != nil {
			return nil, err
		}
	}
	return active, nil
}

func (s *Service) activeDeploymentsForApp(appID string) ([]ActiveDeployment, error) {
	active, err := s.store.ActiveDeploymentsForApp(appID)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return active, nil
	}
	for i := range active {
		if err := s.hydrateArtifact(&active[i]); err != nil {
			return nil, err
		}
	}
	return active, nil
}

func (s *Service) runtimeDeploymentsForApp(appID string) ([]ActiveDeployment, error) {
	s.runtimeDeploymentsMu.RLock()
	active, ok := s.runtimeDeployments[appID]
	version := s.runtimeVersions[appID]
	s.runtimeDeploymentsMu.RUnlock()
	if ok {
		return active, nil
	}

	active, err := s.activeDeploymentsForApp(appID)
	if err != nil {
		return nil, err
	}
	s.runtimeDeploymentsMu.Lock()
	if s.runtimeVersions[appID] == version {
		if cached, exists := s.runtimeDeployments[appID]; exists {
			active = cached
		} else {
			s.runtimeDeployments[appID] = active
		}
	}
	s.runtimeDeploymentsMu.Unlock()
	return active, nil
}

func (s *Service) invalidateRuntimeDeployments(appID string) {
	s.runtimeDeploymentsMu.Lock()
	delete(s.runtimeDeployments, appID)
	s.runtimeVersions[appID]++
	s.runtimeDeploymentsMu.Unlock()
}

func (s *Service) hydrateDeploymentFiles(deployment *Deployment) error {
	if len(deployment.Files) > 0 {
		if deployment.BundleSize == 0 {
			deployment.BundleSize = bundleSize(deployment.Files)
		}
		return nil
	}
	if deployment.ObjectKey == "" {
		return nil
	}
	if s.objects == nil {
		return errors.New("object storage is not configured")
	}
	object, err := s.objects.Get(deployment.AppID, deployment.ObjectKey)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(object.Body, &deployment.Files); err != nil {
		return err
	}
	if deployment.BundleSize == 0 {
		deployment.BundleSize = bundleSize(deployment.Files)
	}
	return nil
}

func (s *Service) resolvedSecretValues(appID string) (map[string]string, error) {
	if artifact, managed, err := s.managedLatestArtifact(appID); err != nil {
		return nil, err
	} else if managed {
		return s.artifactSecretValues(artifact)
	}
	records, err := s.store.ListSecrets(appID)
	if err != nil {
		if errors.Is(err, ErrAppNotFound) {
			return nil, err
		}
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	if s.secrets == nil {
		return nil, errors.New("NANOFLARE_SECRET_KEY is required for secret operations")
	}
	values := make(map[string]string, len(records))
	for _, record := range records {
		value, err := s.secrets.Decrypt(record.Nonce, record.Ciphertext)
		if err != nil {
			return nil, err
		}
		values[record.Name] = value
	}
	return values, nil
}

// Pin legacy active revisions before changing mutable secret records.
func (s *Service) pinActiveArtifacts(appID string) error {
	active, err := s.store.ActiveDeploymentsForApp(appID)
	if err != nil {
		return err
	}
	for _, item := range active {
		if _, err := s.freezeArtifact(item, nil); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) rolloutSecretsIfActive(appID string) error {
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	previous := activeTrafficForApp(active, appID)
	if len(previous) == 0 {
		return nil
	}
	nextTraffic := []DeploymentTraffic{}
	for _, current := range active {
		if current.App.ID != appID {
			continue
		}
		next := cloneArtifact(DeploymentArtifact{Deployment: current.Deployment, ObjectKey: current.Deployment.ObjectKey, BundleSize: current.Deployment.BundleSize}).Deployment
		next.ID, err = randomToken()
		if err != nil {
			return err
		}
		next.Port = 0
		next.CreatedAt = time.Now().UTC()
		base, e := s.store.GetDeploymentArtifact(appID, current.Deployment.ID)
		if e != nil {
			return e
		}
		records, e := s.store.ListSecrets(appID)
		if e != nil {
			return e
		}
		base.Secrets = sealRecords(records)
		if _, err = s.freezeArtifact(ActiveDeployment{App: current.App, Deployment: next}, &base); err != nil {
			return err
		}
		if err = s.store.StoreDeployment(next); err != nil {
			return err
		}
		nextTraffic = append(nextTraffic, DeploymentTraffic{ID: next.ID, TrafficPercent: current.TrafficPercent})
	}
	key, err := randomToken()
	if err != nil {
		return err
	}
	p := Publication{AppID: appID, Key: "secret-" + key, Fingerprint: key, DeploymentID: nextTraffic[0].ID, Stage: "stored", PreviousTraffic: previous, TargetTraffic: nextTraffic, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	artifact, err := s.store.GetDeploymentArtifact(appID, p.DeploymentID)
	if err != nil {
		return err
	}
	p.Digest = artifact.Digest
	if err = s.store.SavePublication(p); err != nil {
		return err
	}
	if _, err = s.publishStoredArtifact(&p, artifact); err != nil {
		return err
	}
	return nil
}

func (s *Service) deploymentObjectReferenced(appID, key string) bool {
	records, err := s.store.ListDeployments()
	if err != nil {
		return true
	}
	for _, record := range records {
		if record.Deployment.AppID != appID {
			continue
		}
		if record.Deployment.ObjectKey == key {
			return true
		}
		for _, asset := range record.Deployment.Assets {
			if asset.ObjectKey == key {
				return true
			}
		}
	}
	return false
}

func (s *Service) cleanupDeploymentObject(deployment Deployment) {
	if s.objects == nil || deployment.ObjectKey == "" {
		return
	}
	if !s.deploymentObjectReferenced(deployment.AppID, deployment.ObjectKey) {
		_ = s.objects.Delete(deployment.AppID, deployment.ObjectKey)
	}
}

func (s *Service) cleanupDeploymentAssets(deployment Deployment) {
	if s.objects == nil {
		return
	}
	for _, asset := range deployment.Assets {
		if asset.ObjectKey == "" {
			continue
		}
		if !s.deploymentObjectReferenced(deployment.AppID, asset.ObjectKey) {
			_ = s.objects.Delete(deployment.AppID, asset.ObjectKey)
		}
	}
}

func (s *Service) rollbackDeployment(appID string, previousTraffic []DeploymentTraffic, deployment Deployment) error {
	defer s.invalidateRuntimeDeployments(appID)
	if err := s.store.SetActiveTraffic(appID, previousTraffic); err != nil {
		return err
	}
	if err := s.store.DeleteDeployment(deployment.ID); err != nil {
		return err
	}
	s.cleanupDeploymentObject(deployment)
	s.cleanupDeploymentAssets(deployment)
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	if err := s.writer.Write(active); err != nil {
		return fmt.Errorf("restore generated config: %w", err)
	}
	return nil
}

func bundleSize(files []WorkerFile) int64 {
	var total int64
	for _, file := range files {
		total += file.Size
	}
	return total
}

func deploymentBundleObjectPath(deploymentID string) string {
	return path.Join("deployments", deploymentID, "bundle.json")
}

func deploymentAssetObjectPath(deploymentID, assetPath string) string {
	return path.Join("deployments", deploymentID, "assets", assetPath)
}

const objectStorageBucketScope = "object-storage-buckets"

func objectStorageBucketPath(bucketID, objectPath string) string {
	// Normalize the user key within its own root before joining the authorized
	// bucket prefix. Cleaning the combined path could traverse into another bucket.
	key := strings.TrimPrefix(path.Clean("/"+objectPath), "/")
	return path.Join("buckets", bucketID, key)
}

func (s *Service) PresignUpload(capability, bucketID, objectPath string) (string, error) {
	_, err := s.objectCapabilityAppID(capability, bucketID)
	if err != nil {
		return "", err
	}
	bucket, err := s.GetObjectStorageBucket(bucketID)
	if err != nil {
		return "", err
	}
	if err := s.enforcePresignedObjectUploadAllowed(bucket.OrgID); err != nil {
		return "", err
	}
	return s.objects.PresignUpload(objectStorageBucketScope, objectStorageBucketPath(bucketID, objectPath), 15*time.Minute)
}

func (s *Service) PresignDownload(capability, bucketID, objectPath string) (string, error) {
	_, err := s.objectCapabilityAppID(capability, bucketID)
	if err != nil {
		return "", err
	}
	return s.objects.PresignDownload(objectStorageBucketScope, objectStorageBucketPath(bucketID, objectPath), 15*time.Minute)
}

func (s *Service) DeleteObject(capability, bucketID, objectPath string) error {
	return s.DeleteObjectContext(context.Background(), capability, bucketID, objectPath)
}

func (s *Service) DeleteObjectContext(ctx context.Context, capability, bucketID, objectPath string) error {
	_, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID)
	if err != nil {
		return err
	}
	storedPath := objectStorageBucketPath(bucketID, objectPath)
	var oldSize int64
	existed := false
	if existing, err := s.objectHeadContext(ctx, objectStorageBucketScope, storedPath); err == nil {
		oldSize = existing.Size
		existed = true
	} else if !errors.Is(err, ErrObjectNotFound) {
		return err
	}
	if err := s.objectDeleteContext(ctx, objectStorageBucketScope, storedPath); err != nil {
		return err
	}
	if oldSize > 0 {
		_ = s.store.AdjustObjectStorageBucketSize(bucketID, -oldSize)
	}
	if existed {
		_ = s.store.AdjustObjectStorageBucketObjectCount(bucketID, -1)
	}
	return nil
}

func (s *Service) ObjectPut(capability, bucketID, objectPath, contentType string, data []byte) (ObjectInfo, error) {
	return s.ObjectPutContext(context.Background(), capability, bucketID, objectPath, contentType, data)
}

func (s *Service) ObjectPutContext(ctx context.Context, capability, bucketID, objectPath, contentType string, data []byte) (ObjectInfo, error) {
	_, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID)
	if err != nil {
		return ObjectInfo{}, err
	}
	storedPath := objectStorageBucketPath(bucketID, objectPath)
	var oldSize int64
	existed := false
	if existing, err := s.objectHeadContext(ctx, objectStorageBucketScope, storedPath); err == nil {
		oldSize = existing.Size
		existed = true
	} else if !errors.Is(err, ErrObjectNotFound) {
		return ObjectInfo{}, err
	}
	if err := s.enforceObjectStorageBytesLimit(bucketID, int64(len(data))-oldSize); err != nil {
		return ObjectInfo{}, err
	}
	object, err := s.objectPutContext(ctx, objectStorageBucketScope, storedPath, contentType, data)
	if err != nil {
		return ObjectInfo{}, err
	}
	_ = s.store.AdjustObjectStorageBucketSize(bucketID, object.Size-oldSize)
	if !existed {
		_ = s.store.AdjustObjectStorageBucketObjectCount(bucketID, 1)
	}
	object.Key = strings.TrimPrefix(objectPath, "/")
	return object, nil
}

func (s *Service) ObjectGet(capability, bucketID, objectPath string) (ObjectBody, bool, error) {
	return s.ObjectGetContext(context.Background(), capability, bucketID, objectPath)
}

func (s *Service) ObjectGetContext(ctx context.Context, capability, bucketID, objectPath string) (ObjectBody, bool, error) {
	_, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID)
	if err != nil {
		return ObjectBody{}, false, err
	}
	object, err := s.objectGetContext(ctx, objectStorageBucketScope, objectStorageBucketPath(bucketID, objectPath))
	if errors.Is(err, ErrObjectNotFound) {
		return ObjectBody{}, false, nil
	}
	object.Key = strings.TrimPrefix(objectPath, "/")
	return object, err == nil, err
}

func (s *Service) ObjectHead(capability, bucketID, objectPath string) (ObjectInfo, bool, error) {
	return s.ObjectHeadContext(context.Background(), capability, bucketID, objectPath)
}

func (s *Service) ObjectHeadContext(ctx context.Context, capability, bucketID, objectPath string) (ObjectInfo, bool, error) {
	_, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID)
	if err != nil {
		return ObjectInfo{}, false, err
	}
	object, err := s.objectHeadContext(ctx, objectStorageBucketScope, objectStorageBucketPath(bucketID, objectPath))
	if errors.Is(err, ErrObjectNotFound) {
		return ObjectInfo{}, false, nil
	}
	object.Key = strings.TrimPrefix(objectPath, "/")
	return object, err == nil, err
}

func (s *Service) ObjectList(capability, bucketID string) ([]ObjectInfo, error) {
	_, err := s.objectCapabilityAppID(capability, bucketID)
	if err != nil {
		return nil, err
	}
	objects, err := s.objects.List(objectStorageBucketScope, objectStorageBucketPath(bucketID, ""))
	if err != nil {
		return nil, err
	}
	prefix := objectStorageBucketPath(bucketID, "") + "/"
	for i := range objects {
		objects[i].Key = strings.TrimPrefix(objects[i].Key, prefix)
	}
	return objects, nil
}

func (s *Service) appIDForCapability(capability string) (string, error) {
	if s.objects == nil {
		return "", errors.New("object storage is not configured")
	}
	return s.store.AppIDForCapability(capability)
}

func (s *Service) KVGet(capability, namespaceID, key string) ([]byte, bool, error) {
	var err error
	capability, err = s.runtimeKVCapability(capability, namespaceID)
	if err != nil {
		return nil, false, err
	}
	return s.store.KVGet(capability, namespaceID, key)
}

func (s *Service) KVPut(capability, namespaceID, key string, value []byte) error {
	return s.KVPutWithOptions(capability, namespaceID, key, value, RuntimeKVPutOptions{})
}

func (s *Service) KVPutWithOptions(capability, namespaceID, key string, value []byte, options RuntimeKVPutOptions) error {
	var err error
	capability, err = s.runtimeKVCapability(capability, namespaceID)
	if err != nil {
		return err
	}
	oldSize, _, err := s.kvValueSize(capability, namespaceID, key)
	if err != nil {
		return err
	}
	delta := int64(len(value)) - oldSize
	orgID, err := s.enforceKVStorageBytesLimit(namespaceID, delta)
	if err != nil {
		return err
	}
	if optimized, ok := s.store.(kvWriteOptimizer); ok {
		appliedDelta, err := optimized.KVPutWithSizeDelta(capability, namespaceID, key, value, options)
		if err != nil {
			s.invalidateKVOrgUsageCache(orgID)
			return err
		}
		if appliedDelta != delta {
			s.invalidateKVOrgUsageCache(orgID)
		} else {
			s.adjustKVOrgUsageCache(orgID, appliedDelta)
		}
		return nil
	}
	if err := s.store.KVPut(capability, namespaceID, key, value, options); err != nil {
		s.invalidateKVOrgUsageCache(orgID)
		return err
	}
	if err := s.store.AdjustKVNamespaceSize(namespaceID, delta); err != nil {
		s.invalidateKVOrgUsageCache(orgID)
		return err
	}
	s.adjustKVOrgUsageCache(orgID, delta)
	return nil
}

func (s *Service) KVListPage(capability, namespaceID string, options RuntimeKVListOptions) (RuntimeKVListResult, error) {
	var err error
	capability, err = s.runtimeKVCapability(capability, namespaceID)
	if err != nil {
		return RuntimeKVListResult{}, err
	}
	return s.store.KVListPage(capability, namespaceID, options)
}

func (s *Service) KVDelete(capability, namespaceID, key string) error {
	var err error
	capability, err = s.runtimeKVCapability(capability, namespaceID)
	if err != nil {
		return err
	}
	oldSize, ok, err := s.kvValueSize(capability, namespaceID, key)
	if err != nil {
		return err
	}
	orgID, orgErr := s.kvNamespaceOrgID(namespaceID)
	if orgErr != nil {
		return orgErr
	}
	if optimized, hasOptimizer := s.store.(kvWriteOptimizer); hasOptimizer {
		delta, err := optimized.KVDeleteWithSizeDelta(capability, namespaceID, key)
		if err != nil {
			s.invalidateKVOrgUsageCache(orgID)
			return err
		}
		logicalDelta := int64(0)
		if ok {
			logicalDelta = -oldSize
		}
		if delta != logicalDelta {
			s.invalidateKVOrgUsageCache(orgID)
		} else {
			s.adjustKVOrgUsageCache(orgID, delta)
		}
		return nil
	}
	if err := s.store.KVDelete(capability, namespaceID, key); err != nil {
		s.invalidateKVOrgUsageCache(orgID)
		return err
	}
	if ok {
		delta := -oldSize
		if err := s.store.AdjustKVNamespaceSize(namespaceID, delta); err != nil {
			s.invalidateKVOrgUsageCache(orgID)
			return err
		}
		s.adjustKVOrgUsageCache(orgID, delta)
	}
	return nil
}

func (s *Service) WorkerKVList(appID, namespaceID string) ([]WorkerKVKey, error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureActiveDeploymentBindsNamespace(appID, namespaceID); err != nil {
		return nil, err
	}
	return s.store.KVList(app.RuntimeToken, namespaceID)
}

func (s *Service) WorkerKVListForOrg(orgID, appID, namespaceID string) ([]WorkerKVKey, error) {
	if err := s.ensureAppAndKVNamespaceInOrg(orgID, appID, namespaceID); err != nil {
		return nil, err
	}
	return s.WorkerKVList(appID, namespaceID)
}

func (s *Service) WorkerKVGet(appID, namespaceID, key string) ([]byte, bool, error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return nil, false, err
	}
	if err := s.ensureActiveDeploymentBindsNamespace(appID, namespaceID); err != nil {
		return nil, false, err
	}
	return s.store.KVGet(app.RuntimeToken, namespaceID, key)
}

func (s *Service) WorkerKVGetForOrg(orgID, appID, namespaceID, key string) ([]byte, bool, error) {
	if err := s.ensureAppAndKVNamespaceInOrg(orgID, appID, namespaceID); err != nil {
		return nil, false, err
	}
	return s.WorkerKVGet(appID, namespaceID, key)
}

func (s *Service) WorkerKVPut(appID, namespaceID, key string, value []byte) error {
	app, _, err := s.worker(appID)
	if err != nil {
		return err
	}
	if err := s.ensureActiveDeploymentBindsNamespace(appID, namespaceID); err != nil {
		return err
	}
	return s.KVPut(app.RuntimeToken, namespaceID, key, value)
}

func (s *Service) WorkerKVPutForOrg(orgID, appID, namespaceID, key string, value []byte) error {
	if err := s.ensureAppAndKVNamespaceInOrg(orgID, appID, namespaceID); err != nil {
		return err
	}
	return s.WorkerKVPut(appID, namespaceID, key, value)
}

func (s *Service) WorkerKVDelete(appID, namespaceID, key string) error {
	app, _, err := s.worker(appID)
	if err != nil {
		return err
	}
	if err := s.ensureActiveDeploymentBindsNamespace(appID, namespaceID); err != nil {
		return err
	}
	return s.KVDelete(app.RuntimeToken, namespaceID, key)
}

func (s *Service) WorkerKVDeleteForOrg(orgID, appID, namespaceID, key string) error {
	if err := s.ensureAppAndKVNamespaceInOrg(orgID, appID, namespaceID); err != nil {
		return err
	}
	return s.WorkerKVDelete(appID, namespaceID, key)
}

// KVNamespaceListForOrg provides console access to keys in a namespace without
// requiring the namespace to be bound to a worker. Organization ownership is
// verified before the store is accessed.
func (s *Service) KVNamespaceListForOrg(orgID, namespaceID string) ([]WorkerKVKey, error) {
	if _, err := s.GetKVNamespaceForOrg(orgID, namespaceID); err != nil {
		return nil, err
	}
	return s.store.KVList("", namespaceID)
}

func (s *Service) KVNamespaceGetForOrg(orgID, namespaceID, key string) ([]byte, bool, error) {
	if _, err := s.GetKVNamespaceForOrg(orgID, namespaceID); err != nil {
		return nil, false, err
	}
	return s.store.KVGet("", namespaceID, key)
}

func (s *Service) KVNamespacePutForOrg(orgID, namespaceID, key string, value []byte) error {
	if _, err := s.GetKVNamespaceForOrg(orgID, namespaceID); err != nil {
		return err
	}
	return s.KVPut("", namespaceID, key, value)
}

func (s *Service) KVNamespaceDeleteForOrg(orgID, namespaceID, key string) error {
	if _, err := s.GetKVNamespaceForOrg(orgID, namespaceID); err != nil {
		return err
	}
	return s.KVDelete("", namespaceID, key)
}

func (s *Service) DBExecute(capability, databaseID string, request DBQueryRequest) (DBQueryResponse, error) {
	return s.DBExecuteContext(context.Background(), capability, databaseID, request)
}
func (s *Service) DBExecuteContext(ctx context.Context, capability, databaseID string, request DBQueryRequest) (DBQueryResponse, error) {
	if isRevisionCapability(capability) {
		if _, err := s.AuthorizeRuntime(ctx, capability, RuntimeDatabase, databaseID); err != nil {
			return DBQueryResponse{}, err
		}
		if s.db == nil {
			return DBQueryResponse{}, errors.New("database runtime is not configured")
		}
		return s.executeDBWithMetricsContext(ctx, databaseID, request)
	}
	appID, err := s.store.AppIDForCapability(capability)
	if err != nil {
		return DBQueryResponse{}, err
	}
	if err := s.ensureActiveDeploymentBindsDatabase(appID, databaseID); err != nil {
		return DBQueryResponse{}, err
	}
	if s.db == nil {
		return DBQueryResponse{}, errors.New("database runtime is not configured")
	}
	return s.executeDBWithMetricsContext(ctx, databaseID, request)
}

func (s *Service) WorkerDBExecuteForOrg(orgID, databaseID string, request DBQueryRequest) (DBQueryResponse, error) {
	if _, err := s.GetDatabaseForOrg(orgID, databaseID); err != nil {
		return DBQueryResponse{}, err
	}
	if s.db == nil {
		return DBQueryResponse{}, errors.New("database runtime is not configured")
	}
	return s.executeDBWithMetrics(databaseID, request)
}

func (s *Service) ApplyDBMigrationForOrg(orgID, databaseID, name, sql string) (DBMigrationResult, error) {
	if _, err := s.GetDatabaseForOrg(orgID, databaseID); err != nil {
		return DBMigrationResult{}, err
	}
	if s.db == nil {
		return DBMigrationResult{}, errors.New("database runtime is not configured")
	}
	return s.db.ApplyMigration(databaseID, name, sql)
}

func (s *Service) DatabaseMetrics(databaseID string) (DatabaseMetrics, error) {
	databaseID = strings.TrimSpace(databaseID)
	if databaseID == "" {
		return DatabaseMetrics{}, ErrDatabaseNotFound
	}
	if s.db != nil {
		if stats, err := s.db.Stats(databaseID); err == nil {
			_ = s.store.UpdateDatabaseRuntimeStats(databaseID, stats)
		}
	}
	return s.store.DatabaseMetrics(databaseID)
}

func (s *Service) DatabaseMetricsForOrg(orgID, databaseID string) (DatabaseMetrics, error) {
	if _, err := s.GetDatabaseForOrg(orgID, databaseID); err != nil {
		return DatabaseMetrics{}, err
	}
	return s.DatabaseMetrics(databaseID)
}

func (s *Service) DatabaseMetricsTimeseries(databaseID string) (DatabaseMetricsTimeseries, error) {
	databaseID = strings.TrimSpace(databaseID)
	if databaseID == "" {
		return DatabaseMetricsTimeseries{}, ErrDatabaseNotFound
	}
	if _, err := s.GetDatabase(databaseID); err != nil {
		return DatabaseMetricsTimeseries{}, err
	}
	if s.dbTimeseries == nil {
		return DatabaseMetricsTimeseries{}, nil
	}
	return s.dbTimeseries.DatabaseMetricsTimeseries(databaseID)
}

func (s *Service) DatabaseMetricsTimeseriesForOrg(orgID, databaseID string) (DatabaseMetricsTimeseries, error) {
	if _, err := s.GetDatabaseForOrg(orgID, databaseID); err != nil {
		return DatabaseMetricsTimeseries{}, err
	}
	return s.DatabaseMetricsTimeseries(databaseID)
}

func (s *Service) DatabaseReplicationStatusForOrg(orgID, databaseID string) (DatabaseReplicationStatus, error) {
	if _, err := s.GetDatabaseForOrg(orgID, databaseID); err != nil {
		return DatabaseReplicationStatus{}, err
	}
	if s.db == nil {
		return DatabaseReplicationStatus{State: "disabled", RecoveryHours: 168}, nil
	}
	return s.db.ReplicationStatus(databaseID)
}

func (s *Service) RestoreDatabaseForOrg(orgID, databaseID string, timestamp *time.Time) (RestoreDatabaseResult, error) {
	if _, err := s.GetDatabaseForOrg(orgID, databaseID); err != nil {
		s.recordRestore(databaseID, "error")
		return RestoreDatabaseResult{}, err
	}
	if s.db == nil {
		s.recordRestore(databaseID, "error")
		return RestoreDatabaseResult{}, errors.New("database runtime is not configured")
	}
	result, err := s.db.Restore(databaseID, timestamp)
	if err != nil {
		s.recordRestore(databaseID, "error")
	} else {
		s.recordRestore(databaseID, "success")
	}
	return result, err
}

func (s *Service) recordRestore(databaseID, result string) {
	key := databaseID + "\x00" + result
	value, _ := s.restoreCounts.LoadOrStore(key, &atomic.Uint64{})
	value.(*atomic.Uint64).Add(1)
}
func (s *Service) DatabaseRestoreCount(databaseID, result string) uint64 {
	value, ok := s.restoreCounts.Load(databaseID + "\x00" + result)
	if !ok {
		return 0
	}
	return value.(*atomic.Uint64).Load()
}

func (s *Service) KVNamespaceMetricsTimeseriesForOrg(orgID, namespaceID string) (KVNamespaceMetricsTimeseries, error) {
	if _, err := s.GetKVNamespaceForOrg(orgID, namespaceID); err != nil {
		return KVNamespaceMetricsTimeseries{}, err
	}
	if s.kvTimeseries == nil {
		return KVNamespaceMetricsTimeseries{}, nil
	}
	return s.kvTimeseries.KVNamespaceMetricsTimeseries(namespaceID)
}

func (s *Service) ObjectStorageBucketMetricsTimeseriesForOrg(orgID, bucketID string) (ObjectStorageBucketMetricsTimeseries, error) {
	if _, err := s.GetObjectStorageBucketForOrg(orgID, bucketID); err != nil {
		return ObjectStorageBucketMetricsTimeseries{}, err
	}
	if s.objectTimeseries == nil {
		return ObjectStorageBucketMetricsTimeseries{}, nil
	}
	return s.objectTimeseries.ObjectStorageBucketMetricsTimeseries(bucketID)
}

func (s *Service) WorkerMetricsTimeseriesForOrg(orgID, appID string) (WorkerMetricsTimeseries, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return WorkerMetricsTimeseries{}, err
	}
	if s.workerTimeseries == nil {
		return WorkerMetricsTimeseries{}, nil
	}
	return s.workerTimeseries.WorkerMetricsTimeseries(appID)
}

func (s *Service) OrganizationWorkerMetricsTimeseries(orgID string) (WorkerMetricsTimeseries, error) {
	workers, err := s.ListAppsForOrg(orgID)
	if err != nil {
		return WorkerMetricsTimeseries{}, err
	}
	reader, ok := s.workerTimeseries.(OrganizationWorkerMetricsTimeseriesReader)
	if !ok {
		return WorkerMetricsTimeseries{}, nil
	}
	workerIDs := make([]string, 0, len(workers))
	for _, worker := range workers {
		workerIDs = append(workerIDs, worker.ID)
	}
	return reader.OrganizationWorkerMetricsTimeseries(workerIDs)
}

func (s *Service) executeDBWithMetrics(databaseID string, request DBQueryRequest) (DBQueryResponse, error) {
	return s.executeDBWithMetricsContext(context.Background(), databaseID, request)
}
func (s *Service) executeDBWithMetricsContext(ctx context.Context, databaseID string, request DBQueryRequest) (DBQueryResponse, error) {
	var response DBQueryResponse
	var err error
	if executor, ok := s.db.(ContextDBExecutor); ok {
		response, err = executor.ExecuteContext(ctx, databaseID, request)
	} else {
		response, err = s.db.Execute(databaseID, request)
	}
	if err != nil {
		return DBQueryResponse{}, err
	}
	s.recordDBQueryMetrics(databaseID, request, response)
	return response, nil
}

func (s *Service) recordDBQueryMetrics(databaseID string, request DBQueryRequest, response DBQueryResponse) {
	stats := DatabaseRuntimeStats{TableCount: -1}
	if s.db != nil {
		if nextStats, err := s.db.Stats(databaseID); err == nil {
			stats = nextStats
			_ = s.store.UpdateDatabaseRuntimeStats(databaseID, stats)
		}
	}
	method := strings.TrimSpace(request.Method)
	if method == "" {
		method = "run"
	}
	statements := request.Statements
	if len(response.Results) > 0 {
		for i, result := range response.Results {
			sql := ""
			if i < len(statements) {
				sql = statements[i].SQL
			}
			s.recordDBResultMetrics(databaseID, sql, result, stats)
		}
		return
	}
	if response.Exec != nil {
		sql := ""
		if len(statements) > 0 {
			sql = statements[0].SQL
		}
		changed := dbStatementLooksLikeWrite(sql)
		_ = s.store.RecordDatabaseQueryMetrics(DatabaseQueryMetricsInput{
			DatabaseID: databaseID,
			DurationMS: response.Exec.Duration,
			ChangedDB:  changed,
			SizeAfter:  stats.StorageBytes,
			TableCount: stats.TableCount,
		})
	}
}

func (s *Service) recordDBResultMetrics(databaseID, sql string, result D1Result, stats DatabaseRuntimeStats) {
	if !result.Success {
		return
	}
	rowsReturned := int64(len(result.Results))
	rowsWritten := result.Meta.RowsWritten
	changed := result.Meta.ChangedDB || rowsWritten > 0 || dbStatementLooksLikeWrite(sql)
	sizeAfter := result.Meta.SizeAfter
	if sizeAfter <= 0 {
		sizeAfter = stats.StorageBytes
	}
	_ = s.store.RecordDatabaseQueryMetrics(DatabaseQueryMetricsInput{
		DatabaseID:   databaseID,
		DurationMS:   result.Meta.Duration,
		RowsRead:     result.Meta.RowsRead,
		RowsReturned: rowsReturned,
		RowsWritten:  rowsWritten,
		ChangedDB:    changed,
		SizeAfter:    sizeAfter,
		TableCount:   stats.TableCount,
	})
}

func dbStatementLooksLikeWrite(sql string) bool {
	sql = strings.ToUpper(strings.TrimSpace(sql))
	for _, prefix := range []string{"INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "REPLACE", "VACUUM", "PRAGMA"} {
		if strings.HasPrefix(sql, prefix) {
			return true
		}
	}
	return false
}

func (s *Service) WorkerObjectList(appID, bucketID string) ([]ObjectInfo, error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureActiveDeploymentBindsObjectStorageBucket(appID, bucketID); err != nil {
		return nil, err
	}
	return s.ObjectList(app.RuntimeToken, bucketID)
}

func (s *Service) WorkerObjectListForOrg(orgID, appID, bucketID string) ([]ObjectInfo, error) {
	if err := s.ensureAppAndObjectBucketInOrg(orgID, appID, bucketID); err != nil {
		return nil, err
	}
	return s.WorkerObjectList(appID, bucketID)
}

func (s *Service) WorkerObjectGet(appID, bucketID, key string) (ObjectBody, bool, error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return ObjectBody{}, false, err
	}
	if err := s.ensureActiveDeploymentBindsObjectStorageBucket(appID, bucketID); err != nil {
		return ObjectBody{}, false, err
	}
	return s.ObjectGet(app.RuntimeToken, bucketID, key)
}

func (s *Service) WorkerObjectGetForOrg(orgID, appID, bucketID, key string) (ObjectBody, bool, error) {
	if err := s.ensureAppAndObjectBucketInOrg(orgID, appID, bucketID); err != nil {
		return ObjectBody{}, false, err
	}
	return s.WorkerObjectGet(appID, bucketID, key)
}

func (s *Service) WorkerObjectPut(appID, bucketID, key, contentType string, data []byte) (ObjectInfo, error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := s.ensureActiveDeploymentBindsObjectStorageBucket(appID, bucketID); err != nil {
		return ObjectInfo{}, err
	}
	return s.ObjectPut(app.RuntimeToken, bucketID, key, contentType, data)
}

func (s *Service) WorkerObjectPutForOrg(orgID, appID, bucketID, key, contentType string, data []byte) (ObjectInfo, error) {
	if err := s.ensureAppAndObjectBucketInOrg(orgID, appID, bucketID); err != nil {
		return ObjectInfo{}, err
	}
	return s.WorkerObjectPut(appID, bucketID, key, contentType, data)
}

func (s *Service) WorkerObjectDelete(appID, bucketID, key string) error {
	app, _, err := s.worker(appID)
	if err != nil {
		return err
	}
	if err := s.ensureActiveDeploymentBindsObjectStorageBucket(appID, bucketID); err != nil {
		return err
	}
	return s.DeleteObject(app.RuntimeToken, bucketID, key)
}

func (s *Service) WorkerObjectDeleteForOrg(orgID, appID, bucketID, key string) error {
	if err := s.ensureAppAndObjectBucketInOrg(orgID, appID, bucketID); err != nil {
		return err
	}
	return s.WorkerObjectDelete(appID, bucketID, key)
}

func (s *Service) KVNamespaceMetrics(namespaceID string) (KVNamespaceMetrics, error) {
	return s.store.KVNamespaceMetrics(namespaceID)
}

func (s *Service) KVNamespaceMetricsForOrg(orgID, namespaceID string) (KVNamespaceMetrics, error) {
	if _, err := s.GetKVNamespaceForOrg(orgID, namespaceID); err != nil {
		return KVNamespaceMetrics{}, err
	}
	return s.KVNamespaceMetrics(namespaceID)
}

func (s *Service) ObjectStorageBucketMetrics(bucketID string) (ObjectStorageBucketMetrics, error) {
	metrics, err := s.store.ObjectStorageBucketMetrics(bucketID)
	if err != nil {
		return ObjectStorageBucketMetrics{}, err
	}
	if s.objects == nil {
		return metrics, nil
	}
	size, objectCount, ok, err := s.reconcileObjectStorageBucketUsage(bucketID)
	if err != nil || !ok {
		return metrics, err
	}
	_ = s.store.AdjustObjectStorageBucketSize(bucketID, size-metrics.Size)
	_ = s.store.SetObjectStorageBucketObjectCount(bucketID, objectCount)
	metrics.Size = size
	metrics.ObjectCount = objectCount
	return metrics, nil
}

func (s *Service) ObjectStorageBucketMetricsForOrg(orgID, bucketID string) (ObjectStorageBucketMetrics, error) {
	if _, err := s.GetObjectStorageBucketForOrg(orgID, bucketID); err != nil {
		return ObjectStorageBucketMetrics{}, err
	}
	return s.ObjectStorageBucketMetrics(bucketID)
}

func (s *Service) enforcePresignedObjectUploadAllowed(orgID string) error {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil
	}
	org, err := s.store.GetOrganization(orgID)
	if err != nil {
		return err
	}
	if OrgLimitsForLevel(org.UsageLevel).ObjectStorageBytes != nil {
		return UsageLimitError{Message: NormalizeUsageLevel(org.UsageLevel) + " orgs cannot use presigned object uploads"}
	}
	return nil
}

func (s *Service) enforceObjectStorageBytesLimit(bucketID string, delta int64) error {
	if delta <= 0 {
		return nil
	}
	bucket, err := s.GetObjectStorageBucket(bucketID)
	if err != nil {
		return err
	}
	orgID := strings.TrimSpace(bucket.OrgID)
	if orgID == "" {
		return nil
	}
	org, err := s.store.GetOrganization(orgID)
	if err != nil {
		return err
	}
	limit := OrgLimitsForLevel(org.UsageLevel).ObjectStorageBytes
	if limit == nil {
		return nil
	}
	current, err := s.store.ObjectStorageBytesByOrg(orgID)
	if err != nil {
		return err
	}
	if current+delta > *limit {
		return usageByteLimitError(org.UsageLevel, "object storage", *limit)
	}
	return nil
}

func (s *Service) enforceKVStorageBytesLimit(namespaceID string, delta int64) (string, error) {
	if delta <= 0 {
		return "", nil
	}
	namespace, err := s.GetKVNamespace(namespaceID)
	if err != nil {
		return "", err
	}
	orgID := strings.TrimSpace(namespace.OrgID)
	if orgID == "" {
		return "", nil
	}
	org, err := s.store.GetOrganization(orgID)
	if err != nil {
		return "", err
	}
	limit := OrgLimitsForLevel(org.UsageLevel).KVStorageBytes
	if limit == nil {
		return orgID, nil
	}
	current, err := s.kvStorageBytesByOrg(orgID)
	if err != nil {
		return "", err
	}
	if current+delta > *limit {
		return "", usageByteLimitError(org.UsageLevel, "KV storage", *limit)
	}
	return orgID, nil
}

func (s *Service) RecordRuntimeKVRead(namespaceID string) error {
	return s.store.IncrementKVNamespaceReads(namespaceID)
}

func (s *Service) RecordRuntimeKVWrite(namespaceID string) error {
	return s.store.IncrementKVNamespaceWrites(namespaceID)
}

func (s *Service) kvValueSize(capability, namespaceID, key string) (int64, bool, error) {
	if sizer, ok := s.store.(kvValueSizer); ok {
		return sizer.KVValueSize(capability, namespaceID, key)
	}
	value, ok, err := s.store.KVGet(capability, namespaceID, key)
	if err != nil {
		return 0, false, err
	}
	return int64(len(value)), ok, nil
}

func (s *Service) kvNamespaceOrgID(namespaceID string) (string, error) {
	namespace, err := s.GetKVNamespace(namespaceID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(namespace.OrgID), nil
}

func (s *Service) kvStorageBytesByOrg(orgID string) (int64, error) {
	if s.kvOrgUsageCacheDisabled() || strings.TrimSpace(orgID) == "" {
		return s.store.KVStorageBytesByOrg(orgID)
	}
	now := time.Now()
	s.kvOrgUsageCacheMu.Lock()
	entry, ok := s.kvOrgUsageCache[orgID]
	if ok && now.Before(entry.expiresAt) {
		size := entry.size
		s.kvOrgUsageCacheMu.Unlock()
		return size, nil
	}
	s.kvOrgUsageCacheMu.Unlock()
	size, err := s.store.KVStorageBytesByOrg(orgID)
	if err != nil {
		return 0, err
	}
	s.kvOrgUsageCacheMu.Lock()
	s.kvOrgUsageCache[orgID] = orgKVUsageCacheEntry{size: size, expiresAt: now.Add(s.kvOrgUsageCacheTTL)}
	s.kvOrgUsageCacheMu.Unlock()
	return size, nil
}

func (s *Service) adjustKVOrgUsageCache(orgID string, delta int64) {
	if s.kvOrgUsageCacheDisabled() || strings.TrimSpace(orgID) == "" {
		return
	}
	now := time.Now()
	s.kvOrgUsageCacheMu.Lock()
	defer s.kvOrgUsageCacheMu.Unlock()
	entry, ok := s.kvOrgUsageCache[orgID]
	if !ok {
		return
	}
	entry.size += delta
	if entry.size < 0 {
		entry.size = 0
	}
	entry.expiresAt = now.Add(s.kvOrgUsageCacheTTL)
	s.kvOrgUsageCache[orgID] = entry
}

func (s *Service) invalidateKVOrgUsageCache(orgID string) {
	if s.kvOrgUsageCacheDisabled() || strings.TrimSpace(orgID) == "" {
		return
	}
	s.kvOrgUsageCacheMu.Lock()
	delete(s.kvOrgUsageCache, orgID)
	s.kvOrgUsageCacheMu.Unlock()
}

func (s *Service) kvOrgUsageCacheDisabled() bool {
	disabler, ok := s.store.(kvOrgUsageCacheDisabler)
	return ok && disabler.disableKVOrgUsageCache()
}

func (s *Service) RecordRuntimeObjectRead(bucketID string) error {
	return s.store.IncrementObjectStorageBucketReads(bucketID)
}

func (s *Service) RecordRuntimeObjectWrite(bucketID string) error {
	return s.store.IncrementObjectStorageBucketWrites(bucketID)
}

func (s *Service) reconcileObjectStorageBucketUsage(bucketID string) (int64, int64, bool, error) {
	active, err := s.activeDeployments()
	if err != nil {
		return 0, 0, false, err
	}
	for _, item := range active {
		for _, binding := range item.Deployment.ObjectStorageBuckets {
			if binding.BucketID != bucketID {
				continue
			}
			objects, err := s.ObjectList(item.App.RuntimeToken, bucketID)
			if err != nil {
				return 0, 0, false, err
			}
			var size int64
			for _, object := range objects {
				size += object.Size
			}
			return size, int64(len(objects)), true, nil
		}
	}
	return 0, 0, false, nil
}

func (s *Service) ensureActiveDeploymentBindsNamespace(appID, namespaceID string) error {
	_, active, err := s.worker(appID)
	if err != nil {
		return err
	}
	if active == nil {
		return ErrKVNamespaceNotBound
	}
	for _, binding := range active.Deployment.KVNamespaces {
		if binding.ID == namespaceID {
			return nil
		}
	}
	return ErrKVNamespaceNotBound
}

func (s *Service) ensureAppAndKVNamespaceInOrg(orgID, appID, namespaceID string) error {
	if strings.TrimSpace(orgID) == "" {
		return nil
	}
	app, err := s.appForOrg(orgID, appID)
	if err != nil {
		return err
	}
	namespace, err := s.GetKVNamespaceForOrg(orgID, namespaceID)
	if err != nil {
		return err
	}
	if app.OrgID != namespace.OrgID {
		return ErrKVNamespaceNotFound
	}
	return nil
}

func (s *Service) ensureActiveDeploymentBindsDatabase(appID, databaseID string) error {
	_, active, err := s.worker(appID)
	if err != nil {
		return err
	}
	if active == nil {
		return ErrDatabaseNotBound
	}
	for _, binding := range active.Deployment.Databases {
		if binding.DatabaseID == databaseID {
			return nil
		}
	}
	return ErrDatabaseNotBound
}

func (s *Service) ensureAppAndDatabaseInOrg(orgID, appID, databaseID string) error {
	if strings.TrimSpace(orgID) == "" {
		return nil
	}
	app, err := s.appForOrg(orgID, appID)
	if err != nil {
		return err
	}
	database, err := s.GetDatabaseForOrg(orgID, databaseID)
	if err != nil {
		return err
	}
	if app.OrgID != database.OrgID {
		return ErrDatabaseNotFound
	}
	return nil
}

func (s *Service) ensureAppAndObjectBucketInOrg(orgID, appID, bucketID string) error {
	if strings.TrimSpace(orgID) == "" {
		return nil
	}
	app, err := s.appForOrg(orgID, appID)
	if err != nil {
		return err
	}
	bucket, err := s.GetObjectStorageBucketForOrg(orgID, bucketID)
	if err != nil {
		return err
	}
	if app.OrgID != bucket.OrgID {
		return ErrObjectStorageBucketNotFound
	}
	return nil
}

func (s *Service) ensureActiveDeploymentBindsObjectStorageBucket(appID, bucketID string) error {
	_, active, err := s.worker(appID)
	if err != nil {
		return err
	}
	if active == nil {
		return ErrObjectStorageBucketNotBound
	}
	for _, binding := range active.Deployment.ObjectStorageBuckets {
		if binding.BucketID == bucketID {
			return nil
		}
	}
	return ErrObjectStorageBucketNotBound
}

func (s *Service) ensureCapabilityBindsObjectStorageBucket(appID, bucketID string) error {
	_, active, err := s.worker(appID)
	if err != nil {
		return err
	}
	if active == nil {
		return ErrObjectStorageBucketNotBound
	}
	for _, binding := range active.Deployment.ObjectStorageBuckets {
		if binding.BucketID == bucketID {
			return nil
		}
	}
	return ErrObjectStorageBucketNotBound
}

func (s *Service) AssetFetch(capability, deploymentID, assetPath string) (AssetResponse, error) {
	return s.AssetFetchContext(context.Background(), capability, deploymentID, assetPath)
}

func (s *Service) AssetFetchContext(ctx context.Context, capability, deploymentID, assetPath string) (AssetResponse, error) {
	if err := ctx.Err(); err != nil {
		return AssetResponse{}, err
	}
	if isRevisionCapability(capability) {
		p, err := s.AuthorizeRuntime(ctx, capability, RuntimeAssets, deploymentID)
		if err != nil {
			return AssetResponse{}, err
		}
		return s.assetResponseForDeploymentContext(ctx, p.Deployment, p.App.ID, assetPath)
	}
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return AssetResponse{}, err
	}
	if strings.TrimSpace(deploymentID) == "" {
		return s.assetResponseContext(ctx, appID, assetPath)
	}
	active, err := s.activeDeployments()
	if err != nil {
		return AssetResponse{}, err
	}
	for _, item := range active {
		if item.App.ID == appID && item.Deployment.ID == deploymentID {
			return s.assetResponseForDeploymentContext(ctx, item.Deployment, appID, assetPath)
		}
	}
	return AssetResponse{}, ErrAppNotFound
}

func (s *Service) PublicAsset(appID, requestPath string) (AssetResponse, bool, error) {
	if _, active, err := s.worker(appID); err != nil {
		return AssetResponse{}, false, err
	} else if active == nil {
		return AssetResponse{}, false, nil
	} else {
		return s.PublicAssetForDeployment(*active, requestPath)
	}
}

func (s *Service) PublicAssetForDeployment(active ActiveDeployment, requestPath string) (AssetResponse, bool, error) {
	if active.App.IsPrivate() {
		return AssetResponse{}, false, ErrAppNotFound
	}
	if len(active.Deployment.Assets) == 0 {
		return AssetResponse{}, false, nil
	}
	response, err := s.assetResponseForDeployment(active.Deployment, active.App.ID, requestPath)
	if err != nil {
		return AssetResponse{}, false, err
	}
	return response, true, nil
}

func (s *Service) WorkerPort(appID, requestPath string) (int, bool, error) {
	_, active, err := s.worker(appID)
	if err != nil {
		return 0, false, err
	}
	if active == nil {
		return 0, false, nil
	}
	return active.Deployment.Port, shouldRunWorkerFirst(active.Deployment.AssetConfig.RunWorkerFirst, requestPath), nil
}

func (s *Service) WorkerRuntimeDeployment(appID, requestPath string) (ActiveDeployment, bool, bool, error) {
	return s.WorkerRuntimeDeploymentWithPreference(appID, requestPath, "")
}

func (s *Service) WorkerRuntimeDeploymentWithPreference(appID, requestPath, preferredDeploymentID string) (ActiveDeployment, bool, bool, error) {
	return s.WorkerRuntimeDeploymentWithPreferenceContext(context.Background(), appID, requestPath, preferredDeploymentID)
}

func (s *Service) WorkerRuntimeDeploymentWithPreferenceContext(ctx context.Context, appID, requestPath, preferredDeploymentID string) (ActiveDeployment, bool, bool, error) {
	_, activeSpan := runtimeResolutionTracer.Start(ctx, "runtime.load_active_deployments")
	active, err := s.runtimeDeploymentsForApp(appID)
	if err != nil {
		activeSpan.RecordError(err)
		activeSpan.SetStatus(codes.Error, err.Error())
		activeSpan.End()
		return ActiveDeployment{}, false, false, err
	}
	activeSpan.SetAttributes(attribute.Int("nanoflare.worker.active_deployment.count", len(active)))
	activeSpan.End()
	if len(active) == 0 {
		// Active deployments already include their app. Only query the app
		// separately when there is no deployment, so missing workers remain
		// distinguishable from registered workers that have not deployed yet.
		_, workerSpan := runtimeResolutionTracer.Start(ctx, "runtime.load_worker")
		_, err := s.store.GetApp(appID)
		if err != nil {
			workerSpan.RecordError(err)
			workerSpan.SetStatus(codes.Error, err.Error())
		}
		workerSpan.End()
		return ActiveDeployment{}, false, false, err
	}

	selected := selectWeightedDeploymentWithPreference(active, strings.TrimSpace(preferredDeploymentID))
	if selected == nil {
		return ActiveDeployment{}, false, false, nil
	}
	return *selected, shouldRunWorkerFirst(selected.Deployment.AssetConfig.RunWorkerFirst, requestPath), true, nil
}

func shouldRunWorkerFirst(runWorkerFirst RunWorkerFirst, requestPath string) bool {
	if runWorkerFirst.Always() {
		return true
	}
	clean := routeRequestPath(requestPath)
	runFirst := false
	for _, route := range runWorkerFirst.Routes() {
		negated := strings.HasPrefix(route, "!")
		pattern := strings.TrimPrefix(route, "!")
		if routePatternMatches(pattern, clean) {
			runFirst = !negated
		}
	}
	return runFirst
}

func routeRequestPath(requestPath string) string {
	trimmed := strings.TrimSpace(requestPath)
	clean := path.Clean("/" + trimmed)
	if clean == "." {
		clean = "/"
	}
	if strings.HasSuffix(trimmed, "/") && clean != "/" {
		return clean + "/"
	}
	return clean
}

func routePatternMatches(pattern, requestPath string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(requestPath, strings.TrimSuffix(pattern, "*"))
	}
	return requestPath == pattern
}

func (s *Service) assetResponse(appID, requestPath string) (AssetResponse, error) {
	return s.assetResponseContext(context.Background(), appID, requestPath)
}

func (s *Service) assetResponseContext(ctx context.Context, appID, requestPath string) (AssetResponse, error) {
	if s.objects == nil {
		return AssetResponse{}, errors.New("object storage is not configured")
	}
	_, active, err := s.worker(appID)
	if err != nil {
		return AssetResponse{}, err
	}
	if active == nil {
		return AssetResponse{}, ErrAppNotFound
	}
	return s.assetResponseForDeploymentContext(ctx, active.Deployment, appID, requestPath)
}

func (s *Service) assetResponseForDeployment(deployment Deployment, appID, requestPath string) (AssetResponse, error) {
	return s.assetResponseForDeploymentContext(context.Background(), deployment, appID, requestPath)
}

func (s *Service) assetResponseForDeploymentContext(ctx context.Context, deployment Deployment, appID, requestPath string) (AssetResponse, error) {
	if s.objects == nil {
		return AssetResponse{}, errors.New("object storage is not configured")
	}
	if len(deployment.Assets) == 0 {
		return AssetResponse{StatusCode: 404}, nil
	}
	resolved, status, ok := resolveAsset(deployment, requestPath)
	if !ok {
		return AssetResponse{StatusCode: 404}, nil
	}
	object, err := s.objectGetContext(ctx, appID, resolved.ObjectKey)
	if err != nil {
		return AssetResponse{}, err
	}
	artifact, artifactErr := s.store.GetDeploymentArtifact(appID, deployment.ID)
	if artifactErr == nil {
		expected, ok := artifact.AssetDigests[resolved.Path]
		if !ok || expected != hashBytes(object.Body) {
			return AssetResponse{}, errors.New("deployment asset integrity check failed")
		}
	} else if !errors.Is(artifactErr, ErrArtifactNotFound) {
		return AssetResponse{}, artifactErr
	}
	return AssetResponse{
		Body:        object.Body,
		ContentType: resolved.ContentType,
		StatusCode:  status,
	}, nil
}

func resolveAsset(deployment Deployment, requestPath string) (AssetFile, int, bool) {
	lookup := make(map[string]AssetFile, len(deployment.Assets))
	for _, asset := range deployment.Assets {
		lookup["/"+strings.TrimPrefix(asset.Path, "/")] = asset
	}
	clean := path.Clean("/" + strings.TrimSpace(requestPath))
	if clean == "." {
		clean = "/"
	}
	if asset, ok := lookup[clean]; ok {
		return asset, 200, true
	}
	if deployment.AssetConfig.HTMLHandling == "auto-trailing-slash" {
		for _, candidate := range []string{
			htmlIndexPath(clean),
			strings.TrimSuffix(clean, "/") + ".html",
		} {
			if asset, ok := lookup[candidate]; ok {
				return asset, 200, true
			}
		}
	}
	switch deployment.AssetConfig.NotFoundHandling {
	case "single-page-application":
		if asset, ok := lookup["/index.html"]; ok {
			return asset, 200, true
		}
	case "404-page":
		if asset, ok := lookup["/404.html"]; ok {
			return asset, 404, true
		}
	}
	return AssetFile{}, 404, false
}

func htmlIndexPath(requestPath string) string {
	if requestPath == "/" {
		return "/index.html"
	}
	return strings.TrimSuffix(requestPath, "/") + "/index.html"
}

func (s *Service) generatedHostname(name, orgID string, attempt int) (string, error) {
	prefix := slug(name)
	if prefix == "" {
		prefix = "worker"
	}
	org, err := s.organizationHostnameLabel(orgID)
	if err != nil {
		return "", err
	}
	labels := []string{prefix}
	if attempt > 0 {
		suffix, err := s.randomHostnameSuffix()
		if err != nil {
			return "", err
		}
		labels = append(labels, suffix)
	}
	if org != "" {
		labels = append(labels, org)
	}
	generated := strings.Join(labels, "-")
	if strings.Contains(s.baseHostname, "*") {
		return normalizeHostname(strings.Replace(s.baseHostname, "*", generated, 1))
	}
	return normalizeHostname(generated + "." + s.baseHostname)
}

func (s *Service) organizationHostnameLabel(orgID string) (string, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return "", nil
	}
	org, err := s.store.GetOrganization(orgID)
	if err != nil {
		return "", err
	}
	return slug(org.Name), nil
}

func normalizeHostname(hostname string) (string, error) {
	hostname = strings.TrimSuffix(strings.TrimSpace(hostname), ".")
	invalid := errors.New("hostname must be a valid ASCII DNS name without a scheme, path, port, or wildcard")
	if hostname == "" || len(hostname) > 253 || net.ParseIP(hostname) != nil {
		return "", invalid
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", invalid
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return "", invalid
			}
		}
	}
	return strings.ToLower(hostname), nil
}

func normalizeBaseHostname(hostname string) (string, error) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" || strings.Contains(hostname, ":") || net.ParseIP(hostname) != nil {
		return "", errors.New("hostname must be a DNS name without a port")
	}
	if !strings.Contains(hostname, "*") {
		return normalizeHostname(hostname)
	}
	if strings.Count(hostname, "*") != 1 {
		return "", errors.New("base hostname may contain at most one wildcard placeholder")
	}
	labels := strings.Split(hostname, ".")
	if len(labels) == 0 || !strings.Contains(labels[0], "*") {
		return "", errors.New("base hostname wildcard must be in the first DNS label")
	}
	for _, label := range labels[1:] {
		if strings.Contains(label, "*") {
			return "", errors.New("base hostname wildcard must be in the first DNS label")
		}
	}
	if _, err := normalizeHostname(strings.Replace(hostname, "*", "worker", 1)); err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSuffix(hostname, ".")), nil
}

func slug(value string) string {
	var result strings.Builder
	dash := false
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			result.WriteRune(char)
			dash = false
		} else if result.Len() > 0 && !dash {
			result.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func randomHostnameSuffix() (string, error) {
	value := make([]byte, 5)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func randomToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (s *Service) runtimeKVCapability(capability, namespaceID string) (string, error) {
	if !isRevisionCapability(capability) {
		return capability, nil
	}
	principal, err := s.AuthorizeRuntime(context.Background(), capability, RuntimeKV, namespaceID)
	if err != nil {
		return "", err
	}
	return principal.App.RuntimeToken, nil
}
