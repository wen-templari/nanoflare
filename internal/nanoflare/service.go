package nanoflare

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"path"
	"strings"
	"time"
)

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

type WorkerTrafficReader interface {
	Traffic(string) (WorkerTraffic, error)
}

type DatabaseMetricsTimeseriesReader interface {
	DatabaseMetricsTimeseries(string) (DatabaseMetricsTimeseries, error)
}

type Service struct {
	store                Repository
	writer               ConfigWriter
	objects              ObjectStore
	db                   DBExecutor
	output               WorkerOutputReader
	traffic              WorkerTrafficReader
	dbTimeseries         DatabaseMetricsTimeseriesReader
	secrets              *SecretCodec
	baseHostname         string
	randomHostnameSuffix func() (string, error)
}

type AssetResponse struct {
	Body        []byte
	ContentType string
	StatusCode  int
}

func NewService(store Repository, writer ConfigWriter) *Service {
	return &Service{store: store, writer: writer, randomHostnameSuffix: randomHostnameSuffix}
}

func NewServiceWithObjects(store Repository, writer ConfigWriter, objects ObjectStore) *Service {
	return &Service{store: store, writer: writer, objects: objects, randomHostnameSuffix: randomHostnameSuffix}
}

func NewServiceWithConsole(store Repository, writer ConfigWriter, objects ObjectStore, output WorkerOutputReader, traffic WorkerTrafficReader) *Service {
	service := &Service{store: store, writer: writer, objects: objects, output: output, traffic: traffic, randomHostnameSuffix: randomHostnameSuffix}
	if reader, ok := traffic.(DatabaseMetricsTimeseriesReader); ok {
		service.dbTimeseries = reader
	}
	return service
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

func (s *Service) CreateApp(input CreateAppInput) (App, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.OrgID = strings.TrimSpace(input.OrgID)
	input.Hostname = strings.TrimSpace(strings.ToLower(input.Hostname))
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
	app, _, err := s.worker(appID)
	if err != nil {
		return App{}, err
	}
	if input.Auth != nil {
		auth, err := normalizeAuthConfig(*input.Auth)
		if err != nil {
			return App{}, err
		}
		app.Auth = auth
	}
	if err := s.store.UpdateApp(app); err != nil {
		return App{}, err
	}
	active, err := s.activeDeployments()
	if err != nil {
		return App{}, err
	}
	if err := s.writer.Write(active); err != nil {
		return App{}, fmt.Errorf("write generated config: %w", err)
	}
	return app, nil
}

func (s *Service) ListSecrets(appID string) ([]Secret, error) {
	if _, _, err := s.worker(appID); err != nil {
		return nil, err
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
	if item := activeForApp(active, appID); item != nil {
		if err := validateBindingCollisions(item.Deployment.Vars, secretValues, item.Deployment.KVNamespaces, item.Deployment.Databases, item.Deployment.ObjectStorageBuckets, item.Deployment.AssetConfig); err != nil {
			return err
		}
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
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("secret name is required")
	}
	if _, _, err := s.worker(appID); err != nil {
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

func (s *Service) ListAppsForOrg(orgID string) ([]App, error) {
	return s.store.ListAppsByOrg(strings.TrimSpace(orgID))
}

func (s *Service) UpdateAppForOrg(orgID, appID string, input UpdateAppInput) (App, error) {
	app, err := s.appForOrg(orgID, appID)
	if err != nil {
		return App{}, err
	}
	if input.Auth != nil {
		auth, err := normalizeAuthConfig(*input.Auth)
		if err != nil {
			return App{}, err
		}
		app.Auth = auth
	}
	if err := s.store.UpdateApp(app); err != nil {
		return App{}, err
	}
	active, err := s.activeDeployments()
	if err != nil {
		return App{}, err
	}
	if err := s.writer.Write(active); err != nil {
		return App{}, fmt.Errorf("write generated config: %w", err)
	}
	return app, nil
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
	for _, record := range records {
		if record.App.ID != appID {
			continue
		}
		s.cleanupDeploymentObject(record.Deployment)
		s.cleanupDeploymentAssets(record.Deployment)
	}
	if err := s.store.DeleteApp(appID); err != nil {
		return err
	}
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
	app, active, err := s.worker(appID)
	if err != nil {
		return WorkerDetail{}, err
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
		Triggers:             active.Deployment.Triggers,
		Vars:                 cloneVars(active.Deployment.Vars),
		KVNamespaces:         append([]KVBinding(nil), active.Deployment.KVNamespaces...),
		Databases:            append([]DatabaseBinding(nil), active.Deployment.Databases...),
		ObjectStorageBuckets: append([]ObjectStorageBucketBinding(nil), active.Deployment.ObjectStorageBuckets...),
		AssetConfig:          active.Deployment.AssetConfig,
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
	active, err := s.activeDeployments()
	if err != nil {
		_ = s.store.SetActiveTraffic(appID, previousTraffic)
		return nil, err
	}
	if err := s.writer.Write(active); err != nil {
		_ = s.store.SetActiveTraffic(appID, previousTraffic)
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
	return s.output.Output(appID), nil
}

func (s *Service) WorkerOutputForOrg(orgID, appID string) ([]WorkerOutputLine, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return nil, err
	}
	return s.WorkerOutput(appID)
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
	vars, err := normalizeVars(input.Vars)
	if err != nil {
		return Deployment{}, err
	}
	triggers, err := NormalizeTriggers(input.Triggers)
	if err != nil {
		return Deployment{}, err
	}
	secrets, err := s.resolvedSecretValues(appID)
	if err != nil && !errors.Is(err, ErrAppNotFound) {
		return Deployment{}, err
	}
	if err := validateBindingCollisions(vars, secrets, kvNamespaces, databases, objectStorageBuckets, input.AssetConfig); err != nil {
		return Deployment{}, err
	}
	format, err := workerFormat(input.Format, len(files))
	if err != nil {
		return Deployment{}, err
	}
	if _, err := time.Parse("2006-01-02", input.CompatibilityDate); err != nil {
		return Deployment{}, errors.New("compatibility_date must use YYYY-MM-DD")
	}
	compatibilityFlags := normalizeCompatibilityFlags(input.CompatibilityFlags)
	port, err := s.store.NextPort()
	if err != nil {
		return Deployment{}, err
	}
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
		Triggers:             triggers,
		Vars:                 vars,
		KVNamespaces:         kvNamespaces,
		Databases:            databases,
		ObjectStorageBuckets: objectStorageBuckets,
		AssetConfig:          assetConfig,
		BundleSize:           bundleSize(files),
		Port:                 port,
		CreatedAt:            time.Now().UTC(),
	}
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
	activeBefore, err := s.activeDeployments()
	if err != nil {
		s.cleanupDeploymentObject(deployment)
		return Deployment{}, err
	}
	previousTraffic := activeTrafficForApp(activeBefore, appID)
	if err := s.store.Activate(deployment); err != nil {
		s.cleanupDeploymentObject(deployment)
		return Deployment{}, err
	}
	active, err := s.activeDeployments()
	if err != nil {
		if rollbackErr := s.rollbackDeployment(appID, previousTraffic, deployment); rollbackErr != nil {
			return Deployment{}, fmt.Errorf("list active deployments: %w; rollback active deployment: %v", err, rollbackErr)
		}
		return Deployment{}, err
	}
	if err := s.writer.Write(active); err != nil {
		if rollbackErr := s.rollbackDeployment(appID, previousTraffic, deployment); rollbackErr != nil {
			return Deployment{}, fmt.Errorf("write generated config: %w; rollback active deployment: %v", err, rollbackErr)
		}
		return Deployment{}, fmt.Errorf("write generated config: %w", err)
	}
	return deployment, nil
}

func (s *Service) DeployForOrg(orgID, appID string, input DeployInput) (Deployment, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return Deployment{}, err
	}
	for _, binding := range input.KVNamespaces {
		if _, err := s.GetKVNamespaceForOrg(orgID, strings.TrimSpace(binding.ID)); err != nil {
			return Deployment{}, err
		}
	}
	for _, binding := range input.Databases {
		if _, err := s.GetDatabaseForOrg(orgID, strings.TrimSpace(binding.DatabaseID)); err != nil {
			return Deployment{}, err
		}
	}
	for _, binding := range input.ObjectStorageBuckets {
		if _, err := s.GetObjectStorageBucketForOrg(orgID, strings.TrimSpace(binding.BucketID)); err != nil {
			return Deployment{}, err
		}
	}
	return s.Deploy(appID, input)
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

func deploymentFiles(files []WorkerFile, entrypoint string) ([]WorkerFile, string, error) {
	const maxBundleSize = 1 << 20
	if len(files) == 0 {
		return nil, "", errors.New("files are required")
	}
	result := make([]WorkerFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	totalSize := 0
	for _, file := range files {
		file.Path = path.Clean(strings.TrimSpace(file.Path))
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
		if totalSize > maxBundleSize {
			return nil, "", fmt.Errorf("worker files exceed %d byte limit", maxBundleSize)
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
		if file.Size == 0 {
			return nil, fmt.Errorf("asset %q is empty", file.Path)
		}
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
		normalized[name] = append(json.RawMessage(nil), trimmed...)
	}
	return normalized, nil
}

func validateBindingCollisions(vars map[string]json.RawMessage, secrets map[string]string, kvBindings []KVBinding, dbBindings []DatabaseBinding, objectBindings []ObjectStorageBucketBinding, assetConfig AssetConfig) error {
	seen := make(map[string]string)
	add := func(name, kind string) error {
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
	return nil
}

func (s *Service) deploymentBindings(deployment Deployment) []Binding {
	bindings := make([]Binding, 0, len(deployment.KVNamespaces)+len(deployment.Databases)+len(deployment.ObjectStorageBuckets)+1)
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
	if !strings.HasPrefix(route, "/") || route == "/" {
		return fmt.Errorf("auth.protected_routes route %q must be an absolute path and cannot be root", route)
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
		if err := s.hydrateDeploymentFiles(&active[i].Deployment); err != nil {
			return nil, err
		}
		secretValues, err := s.resolvedSecretValues(active[i].App.ID)
		if err != nil {
			return nil, err
		}
		active[i].App.SecretValues = secretValues
	}
	return active, nil
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

func (s *Service) rolloutSecretsIfActive(appID string) error {
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	current := activeForApp(active, appID)
	if current == nil {
		return nil
	}
	port, err := s.store.NextPort()
	if err != nil {
		return err
	}
	deploymentID, err := randomToken()
	if err != nil {
		return err
	}
	next := current.Deployment
	next.ID = deploymentID
	next.Port = port
	next.CreatedAt = time.Now().UTC()
	next.Vars = cloneVars(current.Deployment.Vars)
	next.KVNamespaces = append([]KVBinding(nil), current.Deployment.KVNamespaces...)
	next.CompatibilityFlags = append([]string(nil), current.Deployment.CompatibilityFlags...)
	next.ObjectStorageBuckets = append([]ObjectStorageBucketBinding(nil), current.Deployment.ObjectStorageBuckets...)
	next.Assets = append([]AssetFile(nil), current.Deployment.Assets...)
	next.Files = append([]WorkerFile(nil), current.Deployment.Files...)
	previousTraffic := activeTrafficForApp(active, appID)
	if err := s.store.Activate(next); err != nil {
		return err
	}
	updated, err := s.activeDeployments()
	if err != nil {
		if rollbackErr := s.rollbackDeployment(appID, previousTraffic, next); rollbackErr != nil {
			return fmt.Errorf("list active deployments: %w; rollback active deployment: %v", err, rollbackErr)
		}
		return err
	}
	if err := s.writer.Write(updated); err != nil {
		if rollbackErr := s.rollbackDeployment(appID, previousTraffic, next); rollbackErr != nil {
			return fmt.Errorf("write generated config: %w; rollback active deployment: %v", err, rollbackErr)
		}
		return fmt.Errorf("write generated config: %w", err)
	}
	return nil
}

func (s *Service) cleanupDeploymentObject(deployment Deployment) {
	if s.objects == nil || deployment.ObjectKey == "" {
		return
	}
	_ = s.objects.Delete(deployment.AppID, deployment.ObjectKey)
}

func (s *Service) cleanupDeploymentAssets(deployment Deployment) {
	if s.objects == nil {
		return
	}
	for _, asset := range deployment.Assets {
		if asset.ObjectKey == "" {
			continue
		}
		_ = s.objects.Delete(deployment.AppID, asset.ObjectKey)
	}
}

func (s *Service) rollbackDeployment(appID string, previousTraffic []DeploymentTraffic, deployment Deployment) error {
	if err := s.store.SetActiveTraffic(appID, previousTraffic); err != nil {
		return err
	}
	if err := s.store.DeleteDeployment(deployment.ID); err != nil {
		return err
	}
	s.cleanupDeploymentObject(deployment)
	s.cleanupDeploymentAssets(deployment)
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
	return path.Join("buckets", bucketID, strings.TrimPrefix(objectPath, "/"))
}

func (s *Service) PresignUpload(capability, bucketID, objectPath string) (string, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return "", err
	}
	if err := s.ensureCapabilityBindsObjectStorageBucket(appID, bucketID); err != nil {
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
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return "", err
	}
	if err := s.ensureCapabilityBindsObjectStorageBucket(appID, bucketID); err != nil {
		return "", err
	}
	return s.objects.PresignDownload(objectStorageBucketScope, objectStorageBucketPath(bucketID, objectPath), 15*time.Minute)
}

func (s *Service) DeleteObject(capability, bucketID, objectPath string) error {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return err
	}
	if err := s.ensureCapabilityBindsObjectStorageBucket(appID, bucketID); err != nil {
		return err
	}
	storedPath := objectStorageBucketPath(bucketID, objectPath)
	var oldSize int64
	if existing, err := s.objects.Head(objectStorageBucketScope, storedPath); err == nil {
		oldSize = existing.Size
	} else if !errors.Is(err, ErrObjectNotFound) {
		return err
	}
	if err := s.objects.Delete(objectStorageBucketScope, storedPath); err != nil {
		return err
	}
	if oldSize > 0 {
		_ = s.store.AdjustObjectStorageBucketSize(bucketID, -oldSize)
	}
	return nil
}

func (s *Service) ObjectPut(capability, bucketID, objectPath, contentType string, data []byte) (ObjectInfo, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := s.ensureCapabilityBindsObjectStorageBucket(appID, bucketID); err != nil {
		return ObjectInfo{}, err
	}
	storedPath := objectStorageBucketPath(bucketID, objectPath)
	var oldSize int64
	if existing, err := s.objects.Head(objectStorageBucketScope, storedPath); err == nil {
		oldSize = existing.Size
	} else if !errors.Is(err, ErrObjectNotFound) {
		return ObjectInfo{}, err
	}
	if err := s.enforceObjectStorageBytesLimit(bucketID, int64(len(data))-oldSize); err != nil {
		return ObjectInfo{}, err
	}
	object, err := s.objects.Put(objectStorageBucketScope, storedPath, contentType, data)
	if err != nil {
		return ObjectInfo{}, err
	}
	_ = s.store.AdjustObjectStorageBucketSize(bucketID, object.Size-oldSize)
	object.Key = strings.TrimPrefix(objectPath, "/")
	return object, nil
}

func (s *Service) ObjectGet(capability, bucketID, objectPath string) (ObjectBody, bool, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return ObjectBody{}, false, err
	}
	if err := s.ensureCapabilityBindsObjectStorageBucket(appID, bucketID); err != nil {
		return ObjectBody{}, false, err
	}
	object, err := s.objects.Get(objectStorageBucketScope, objectStorageBucketPath(bucketID, objectPath))
	if errors.Is(err, ErrObjectNotFound) {
		return ObjectBody{}, false, nil
	}
	object.Key = strings.TrimPrefix(objectPath, "/")
	return object, err == nil, err
}

func (s *Service) ObjectHead(capability, bucketID, objectPath string) (ObjectInfo, bool, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return ObjectInfo{}, false, err
	}
	if err := s.ensureCapabilityBindsObjectStorageBucket(appID, bucketID); err != nil {
		return ObjectInfo{}, false, err
	}
	object, err := s.objects.Head(objectStorageBucketScope, objectStorageBucketPath(bucketID, objectPath))
	if errors.Is(err, ErrObjectNotFound) {
		return ObjectInfo{}, false, nil
	}
	object.Key = strings.TrimPrefix(objectPath, "/")
	return object, err == nil, err
}

func (s *Service) ObjectList(capability, bucketID string) ([]ObjectInfo, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return nil, err
	}
	if err := s.ensureCapabilityBindsObjectStorageBucket(appID, bucketID); err != nil {
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
	return s.store.KVGet(capability, namespaceID, key)
}

func (s *Service) KVPut(capability, namespaceID, key string, value []byte) error {
	oldValue, ok, err := s.store.KVGet(capability, namespaceID, key)
	if err != nil {
		return err
	}
	var oldSize int64
	if ok {
		oldSize = int64(len(oldValue))
	}
	if err := s.enforceKVStorageBytesLimit(namespaceID, int64(len(value))-oldSize); err != nil {
		return err
	}
	if err := s.store.KVPut(capability, namespaceID, key, value); err != nil {
		return err
	}
	return s.store.AdjustKVNamespaceSize(namespaceID, int64(len(value))-oldSize)
}

func (s *Service) KVDelete(capability, namespaceID, key string) error {
	oldValue, ok, err := s.store.KVGet(capability, namespaceID, key)
	if err != nil {
		return err
	}
	if err := s.store.KVDelete(capability, namespaceID, key); err != nil {
		return err
	}
	if ok {
		return s.store.AdjustKVNamespaceSize(namespaceID, -int64(len(oldValue)))
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

func (s *Service) DBExecute(capability, databaseID string, request DBQueryRequest) (DBQueryResponse, error) {
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
	return s.executeDBWithMetrics(databaseID, request)
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

func (s *Service) executeDBWithMetrics(databaseID string, request DBQueryRequest) (DBQueryResponse, error) {
	response, err := s.db.Execute(databaseID, request)
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
	size, ok, err := s.reconcileObjectStorageBucketSize(bucketID)
	if err != nil || !ok {
		return metrics, err
	}
	_ = s.store.AdjustObjectStorageBucketSize(bucketID, size-metrics.Size)
	metrics.Size = size
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

func (s *Service) enforceKVStorageBytesLimit(namespaceID string, delta int64) error {
	if delta <= 0 {
		return nil
	}
	namespace, err := s.GetKVNamespace(namespaceID)
	if err != nil {
		return err
	}
	orgID := strings.TrimSpace(namespace.OrgID)
	if orgID == "" {
		return nil
	}
	org, err := s.store.GetOrganization(orgID)
	if err != nil {
		return err
	}
	limit := OrgLimitsForLevel(org.UsageLevel).KVStorageBytes
	if limit == nil {
		return nil
	}
	current, err := s.store.KVStorageBytesByOrg(orgID)
	if err != nil {
		return err
	}
	if current+delta > *limit {
		return usageByteLimitError(org.UsageLevel, "KV storage", *limit)
	}
	return nil
}

func (s *Service) RecordRuntimeKVRead(namespaceID string) error {
	return s.store.IncrementKVNamespaceReads(namespaceID)
}

func (s *Service) RecordRuntimeKVWrite(namespaceID string) error {
	return s.store.IncrementKVNamespaceWrites(namespaceID)
}

func (s *Service) RecordRuntimeObjectRead(bucketID string) error {
	return s.store.IncrementObjectStorageBucketReads(bucketID)
}

func (s *Service) RecordRuntimeObjectWrite(bucketID string) error {
	return s.store.IncrementObjectStorageBucketWrites(bucketID)
}

func (s *Service) reconcileObjectStorageBucketSize(bucketID string) (int64, bool, error) {
	active, err := s.activeDeployments()
	if err != nil {
		return 0, false, err
	}
	for _, item := range active {
		for _, binding := range item.Deployment.ObjectStorageBuckets {
			if binding.BucketID != bucketID {
				continue
			}
			objects, err := s.ObjectList(item.App.RuntimeToken, bucketID)
			if err != nil {
				return 0, false, err
			}
			var size int64
			for _, object := range objects {
				size += object.Size
			}
			return size, true, nil
		}
	}
	return 0, false, nil
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
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return AssetResponse{}, err
	}
	if strings.TrimSpace(deploymentID) == "" {
		return s.assetResponse(appID, assetPath)
	}
	active, err := s.activeDeployments()
	if err != nil {
		return AssetResponse{}, err
	}
	for _, item := range active {
		if item.App.ID == appID && item.Deployment.ID == deploymentID {
			return s.assetResponseForDeployment(item.Deployment, appID, assetPath)
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
	if _, _, err := s.worker(appID); err != nil {
		return ActiveDeployment{}, false, false, err
	}
	active, err := s.activeDeployments()
	if err != nil {
		return ActiveDeployment{}, false, false, err
	}
	selected := selectWeightedDeploymentWithPreference(activeForAppDeployments(active, appID), strings.TrimSpace(preferredDeploymentID))
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
	return s.assetResponseForDeployment(active.Deployment, appID, requestPath)
}

func (s *Service) assetResponseForDeployment(deployment Deployment, appID, requestPath string) (AssetResponse, error) {
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
	object, err := s.objects.Get(appID, resolved.ObjectKey)
	if err != nil {
		return AssetResponse{}, err
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
		return strings.Replace(s.baseHostname, "*", generated, 1), nil
	}
	return generated + "." + s.baseHostname, nil
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
	hostname = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(hostname)), ".")
	if hostname == "" || strings.Contains(hostname, ":") || net.ParseIP(hostname) != nil || strings.Contains(hostname, "*") {
		return "", errors.New("hostname must be a DNS name without a port")
	}
	return hostname, nil
}

func normalizeBaseHostname(hostname string) (string, error) {
	hostname = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(hostname)), ".")
	if hostname == "" || strings.Contains(hostname, ":") || net.ParseIP(hostname) != nil {
		return "", errors.New("hostname must be a DNS name without a port")
	}
	if !strings.Contains(hostname, "*") {
		return hostname, nil
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
	return hostname, nil
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
