package nanoflare

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrAppExists                   = errors.New("app already exists")
	ErrAppNotFound                 = errors.New("app not found")
	ErrInvalidCapability           = errors.New("invalid runtime capability")
	ErrObjectNotFound              = errors.New("object not found")
	ErrKVNamespaceExists           = errors.New("kv namespace already exists")
	ErrKVNamespaceNotFound         = errors.New("kv namespace not found")
	ErrKVNamespaceInUse            = errors.New("kv namespace is still referenced by a deployment")
	ErrKVNamespaceNotBound         = errors.New("kv namespace is not bound by the app's active deployment")
	ErrObjectStorageBucketExists   = errors.New("object storage bucket already exists")
	ErrObjectStorageBucketNotFound = errors.New("object storage bucket not found")
	ErrObjectStorageBucketInUse    = errors.New("object storage bucket is still referenced by a deployment")
	ErrObjectStorageBucketNotBound = errors.New("object storage bucket is not bound by the app's active deployment")
	ErrSecretNotFound              = errors.New("secret not found")
	ErrUserExists                  = errors.New("user already exists")
	ErrUserNotFound                = errors.New("user not found")
	ErrOrganizationExists          = errors.New("organization already exists")
	ErrOrganizationNotFound        = errors.New("organization not found")
	ErrMembershipNotFound          = errors.New("user is not a member of the organization")
)

type Repository interface {
	CreateUser(User) error
	UserByEmail(string) (User, error)
	UserCount() (int, error)
	CreateOrganization(Organization) error
	GetOrganization(string) (Organization, error)
	CountOwnedOrganizationsByUser(userID string) (int, error)
	UpsertOrganizationMembership(OrganizationMembership) error
	OrganizationMembership(userID, orgID string) (OrganizationMembership, error)
	ListOrganizationMembers(orgID string) ([]OrganizationMembership, error)
	OwnerCount(orgID string) (int, error)
	DeleteOrganizationMembership(userID, orgID string) error
	ListOrganizationsForUser(userID string) ([]Organization, error)
	UserBelongsToOrganization(userID, orgID string) (bool, error)
	CreateOrganizationInvite(OrganizationInvite) error
	OrganizationInviteByID(orgID, inviteID string) (OrganizationInvite, error)
	OrganizationInviteByTokenHash(tokenHash string) (OrganizationInvite, error)
	OrganizationInvitesByOrg(orgID string) ([]OrganizationInvite, error)
	UpdateOrganizationInvite(OrganizationInvite) error
	CreateOAuthClient(OAuthClient) error
	CountOAuthClientsByOwnerOrg(string) (int, error)
	OAuthClient(string) (OAuthClient, error)
	OAuthClientsByOwnerOrg(string) ([]OAuthClient, error)
	OAuthClientConnections(clientID string) ([]OAuthClientConnection, error)
	UpdateOAuthClient(OAuthClient) error
	CreateOAuthAuthorizationCode(OAuthAuthorizationCode) error
	OAuthAuthorizationCode(string) (OAuthAuthorizationCode, error)
	UpdateOAuthAuthorizationCode(OAuthAuthorizationCode) error
	CreateOAuthToken(OAuthToken) error
	OAuthAccessToken(string) (OAuthToken, error)
	OAuthRefreshToken(string) (OAuthToken, error)
	UpdateOAuthToken(OAuthToken) error
	OAuthConnections(userID, orgID string) ([]OAuthConnection, error)
	RevokeOAuthClientTokens(userID, orgID, clientID string, revokedAt time.Time) error
	RevokeAllOAuthClientTokens(clientID string, revokedAt time.Time) error
	CreateApp(App) error
	CountAppsByOrg(string) (int, error)
	ListApps() ([]App, error)
	ListAppsByOrg(string) ([]App, error)
	UpdateApp(App) error
	DeleteApp(string) error
	CreateKVNamespace(KVNamespace) error
	CountKVNamespacesByOrg(string) (int, error)
	ListKVNamespaces() ([]KVNamespace, error)
	ListKVNamespacesByOrg(string) ([]KVNamespace, error)
	GetKVNamespace(string) (KVNamespace, error)
	UpdateKVNamespace(KVNamespace) error
	DeleteKVNamespace(string) error
	CreateObjectStorageBucket(ObjectStorageBucket) error
	CountObjectStorageBucketsByOrg(string) (int, error)
	ListObjectStorageBuckets() ([]ObjectStorageBucket, error)
	ListObjectStorageBucketsByOrg(string) ([]ObjectStorageBucket, error)
	GetObjectStorageBucket(string) (ObjectStorageBucket, error)
	UpdateObjectStorageBucket(ObjectStorageBucket) error
	DeleteObjectStorageBucket(string) error
	ListSecrets(string) ([]SecretRecord, error)
	PutSecret(string, SecretRecord) error
	DeleteSecret(string, string) error
	NextPort() (int, error)
	Activate(Deployment) error
	DeleteDeployment(id string) error
	SetActive(appID, deploymentID string) error
	ActiveDeployments() ([]ActiveDeployment, error)
	ListDeployments() ([]DeploymentRecord, error)
	AppIDForCapability(string) (string, error)
	KVList(capability, namespaceID string) ([]WorkerKVKey, error)
	KVGet(capability, namespaceID, key string) ([]byte, bool, error)
	KVPut(capability, namespaceID, key string, value []byte) error
	KVDelete(capability, namespaceID, key string) error
	KVNamespaceMetrics(namespaceID string) (KVNamespaceMetrics, error)
	KVStorageBytesByOrg(orgID string) (int64, error)
	IncrementKVNamespaceReads(namespaceID string) error
	IncrementKVNamespaceWrites(namespaceID string) error
	AdjustKVNamespaceSize(namespaceID string, delta int64) error
	ObjectStorageBucketMetrics(bucketID string) (ObjectStorageBucketMetrics, error)
	ObjectStorageBytesByOrg(orgID string) (int64, error)
	IncrementObjectStorageBucketReads(bucketID string) error
	IncrementObjectStorageBucketWrites(bucketID string) error
	AdjustObjectStorageBucketSize(bucketID string, delta int64) error
}

type Store struct {
	mu              sync.RWMutex
	users           map[string]User
	usersByEmail    map[string]string
	organizations   map[string]Organization
	memberships     map[string]map[string]OrganizationMembership
	invites         map[string]OrganizationInvite
	oauthClients    map[string]OAuthClient
	oauthCodes      map[string]OAuthAuthorizationCode
	oauthTokens     map[string]OAuthToken
	oauthRefresh    map[string]string
	apps            map[string]App
	kvNamespaces    map[string]KVNamespace
	objectBuckets   map[string]ObjectStorageBucket
	secrets         map[string]map[string]SecretRecord
	deployments     map[string][]Deployment
	active          map[string]string
	capabilityToApp map[string]string
	kv              map[string]map[string][]byte
	kvMetrics       map[string]KVNamespaceMetrics
	objectMetrics   map[string]ObjectStorageBucketMetrics
}

func NewStore() *Store {
	return &Store{
		users:           make(map[string]User),
		usersByEmail:    make(map[string]string),
		organizations:   make(map[string]Organization),
		memberships:     make(map[string]map[string]OrganizationMembership),
		invites:         make(map[string]OrganizationInvite),
		oauthClients:    make(map[string]OAuthClient),
		oauthCodes:      make(map[string]OAuthAuthorizationCode),
		oauthTokens:     make(map[string]OAuthToken),
		oauthRefresh:    make(map[string]string),
		apps:            make(map[string]App),
		kvNamespaces:    make(map[string]KVNamespace),
		objectBuckets:   make(map[string]ObjectStorageBucket),
		secrets:         make(map[string]map[string]SecretRecord),
		deployments:     make(map[string][]Deployment),
		active:          make(map[string]string),
		capabilityToApp: make(map[string]string),
		kv:              make(map[string]map[string][]byte),
		kvMetrics:       make(map[string]KVNamespaceMetrics),
		objectMetrics:   make(map[string]ObjectStorageBucketMetrics),
	}
}

func (s *Store) CreateUser(user User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[user.ID]; exists {
		return ErrUserExists
	}
	if _, exists := s.usersByEmail[user.Email]; exists {
		return ErrUserExists
	}
	s.users[user.ID] = user
	s.usersByEmail[user.Email] = user.ID
	return nil
}

func (s *Store) UserByEmail(email string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, exists := s.usersByEmail[email]
	if !exists {
		return User{}, ErrUserNotFound
	}
	return s.users[id], nil
}

func (s *Store) UserCount() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users), nil
}

func (s *Store) CreateOrganization(org Organization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.organizations[org.ID]; exists {
		return ErrOrganizationExists
	}
	org.UsageLevel = NormalizeUsageLevel(org.UsageLevel)
	s.organizations[org.ID] = org
	return nil
}

func (s *Store) GetOrganization(orgID string) (Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	org, exists := s.organizations[orgID]
	if !exists {
		return Organization{}, ErrOrganizationNotFound
	}
	org.UsageLevel = NormalizeUsageLevel(org.UsageLevel)
	return org, nil
}

func (s *Store) CountOwnedOrganizationsByUser(userID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.users[userID]; !exists {
		return 0, ErrUserNotFound
	}
	count := 0
	for _, membership := range s.memberships[userID] {
		if membership.Role == RoleOwner {
			count++
		}
	}
	return count, nil
}

func (s *Store) UpsertOrganizationMembership(membership OrganizationMembership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[membership.UserID]; !exists {
		return ErrUserNotFound
	}
	if _, exists := s.organizations[membership.OrgID]; !exists {
		return ErrOrganizationNotFound
	}
	if s.memberships[membership.UserID] == nil {
		s.memberships[membership.UserID] = make(map[string]OrganizationMembership)
	}
	if membership.CreatedAt.IsZero() {
		if existing, ok := s.memberships[membership.UserID][membership.OrgID]; ok {
			membership.CreatedAt = existing.CreatedAt
		} else {
			membership.CreatedAt = time.Now().UTC()
		}
	}
	membership.Role = NormalizeRole(membership.Role)
	membership.Scopes = append([]string{}, membership.Scopes...)
	s.memberships[membership.UserID][membership.OrgID] = membership
	return nil
}

func (s *Store) AddUserToOrganization(userID, orgID string) error {
	return s.UpsertOrganizationMembership(OrganizationMembership{
		UserID: userID,
		OrgID:  orgID,
		Role:   RoleOwner,
		Scopes: RoleScopes(RoleOwner),
	})
}

func (s *Store) OrganizationMembership(userID, orgID string) (OrganizationMembership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	membership, exists := s.memberships[userID][orgID]
	if !exists {
		return OrganizationMembership{}, ErrMembershipNotFound
	}
	membership.UserEmail = s.users[userID].Email
	membership.Scopes = append([]string{}, membership.Scopes...)
	return membership, nil
}

func (s *Store) ListOrganizationMembers(orgID string) ([]OrganizationMembership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.organizations[orgID]; !exists {
		return nil, ErrOrganizationNotFound
	}
	members := []OrganizationMembership{}
	for userID, orgs := range s.memberships {
		if membership, ok := orgs[orgID]; ok {
			membership.UserEmail = s.users[userID].Email
			membership.Scopes = append([]string{}, membership.Scopes...)
			members = append(members, membership)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].UserEmail < members[j].UserEmail })
	return members, nil
}

func (s *Store) OwnerCount(orgID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, orgs := range s.memberships {
		if membership, ok := orgs[orgID]; ok && membership.Role == RoleOwner {
			count++
		}
	}
	return count, nil
}

func (s *Store) DeleteOrganizationMembership(userID, orgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.memberships[userID][orgID]; !ok {
		return ErrMembershipNotFound
	}
	delete(s.memberships[userID], orgID)
	return nil
}

func (s *Store) ListOrganizationsForUser(userID string) ([]Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.users[userID]; !exists {
		return nil, ErrUserNotFound
	}
	orgs := make([]Organization, 0, len(s.memberships[userID]))
	for orgID, membership := range s.memberships[userID] {
		org := s.organizations[orgID]
		org.UsageLevel = NormalizeUsageLevel(org.UsageLevel)
		org.Role = membership.Role
		org.Scopes = append([]string{}, membership.Scopes...)
		orgs = append(orgs, org)
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].Name < orgs[j].Name })
	return orgs, nil
}

func (s *Store) UserBelongsToOrganization(userID, orgID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.users[userID]; !exists {
		return false, ErrUserNotFound
	}
	_, ok := s.memberships[userID][orgID]
	return ok, nil
}

func (s *Store) CreateOrganizationInvite(invite OrganizationInvite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.organizations[invite.OrgID]; !exists {
		return ErrOrganizationNotFound
	}
	s.invites[invite.TokenHash] = cloneOrganizationInvite(invite)
	return nil
}

func (s *Store) OrganizationInviteByTokenHash(tokenHash string) (OrganizationInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invite, exists := s.invites[tokenHash]
	if !exists {
		return OrganizationInvite{}, ErrInviteNotFound
	}
	invite = cloneOrganizationInvite(invite)
	if org, ok := s.organizations[invite.OrgID]; ok {
		invite.OrgName = org.Name
	}
	if user, ok := s.users[invite.InviterID]; ok {
		invite.InviterEmail = user.Email
	}
	return invite, nil
}

func (s *Store) OrganizationInviteByID(orgID, inviteID string) (OrganizationInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, invite := range s.invites {
		if invite.OrgID == orgID && invite.ID == inviteID {
			invite = cloneOrganizationInvite(invite)
			if org, ok := s.organizations[invite.OrgID]; ok {
				invite.OrgName = org.Name
			}
			if user, ok := s.users[invite.InviterID]; ok {
				invite.InviterEmail = user.Email
			}
			return invite, nil
		}
	}
	return OrganizationInvite{}, ErrInviteNotFound
}

func (s *Store) OrganizationInvitesByOrg(orgID string) ([]OrganizationInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invites := []OrganizationInvite{}
	for _, invite := range s.invites {
		if invite.OrgID == orgID {
			invite = cloneOrganizationInvite(invite)
			if user, ok := s.users[invite.InviterID]; ok {
				invite.InviterEmail = user.Email
			}
			invites = append(invites, invite)
		}
	}
	sort.Slice(invites, func(i, j int) bool { return invites[i].CreatedAt.After(invites[j].CreatedAt) })
	return invites, nil
}

func (s *Store) UpdateOrganizationInvite(invite OrganizationInvite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.invites[invite.TokenHash]; !exists {
		return ErrInviteNotFound
	}
	s.invites[invite.TokenHash] = cloneOrganizationInvite(invite)
	return nil
}

func (s *Store) CreateOAuthClient(client OAuthClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.oauthClients[client.ID]; exists {
		return ErrOAuthClientNotFound
	}
	s.oauthClients[client.ID] = cloneOAuthClient(client)
	return nil
}

func (s *Store) CountOAuthClientsByOwnerOrg(ownerOrgID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, client := range s.oauthClients {
		if client.OwnerOrgID == ownerOrgID {
			count++
		}
	}
	return count, nil
}

func (s *Store) OAuthClient(clientID string) (OAuthClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	client, exists := s.oauthClients[clientID]
	if !exists {
		return OAuthClient{}, ErrOAuthClientNotFound
	}
	return cloneOAuthClient(client), nil
}

func (s *Store) OAuthClientsByOwnerOrg(ownerOrgID string) ([]OAuthClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clients := make([]OAuthClient, 0)
	for _, client := range s.oauthClients {
		if client.OwnerOrgID == ownerOrgID {
			clients = append(clients, cloneOAuthClient(client))
		}
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Name == clients[j].Name {
			return clients[i].ID < clients[j].ID
		}
		return clients[i].Name < clients[j].Name
	})
	return clients, nil
}

func (s *Store) UpdateOAuthClient(client OAuthClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.oauthClients[client.ID]; !exists {
		return ErrOAuthClientNotFound
	}
	s.oauthClients[client.ID] = cloneOAuthClient(client)
	return nil
}

func (s *Store) CreateOAuthAuthorizationCode(code OAuthAuthorizationCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthCodes[code.CodeHash] = cloneOAuthAuthorizationCode(code)
	return nil
}

func (s *Store) OAuthAuthorizationCode(codeHash string) (OAuthAuthorizationCode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	code, exists := s.oauthCodes[codeHash]
	if !exists {
		return OAuthAuthorizationCode{}, ErrOAuthInvalidGrant
	}
	return cloneOAuthAuthorizationCode(code), nil
}

func (s *Store) UpdateOAuthAuthorizationCode(code OAuthAuthorizationCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.oauthCodes[code.CodeHash]; !exists {
		return ErrOAuthInvalidGrant
	}
	s.oauthCodes[code.CodeHash] = cloneOAuthAuthorizationCode(code)
	return nil
}

func (s *Store) CreateOAuthToken(token OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthTokens[token.TokenHash] = cloneOAuthToken(token)
	if token.RefreshTokenHash != "" {
		s.oauthRefresh[token.RefreshTokenHash] = token.TokenHash
	}
	return nil
}

func (s *Store) OAuthAccessToken(tokenHash string) (OAuthToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	token, exists := s.oauthTokens[tokenHash]
	if !exists {
		return OAuthToken{}, ErrOAuthTokenNotFound
	}
	return cloneOAuthToken(token), nil
}

func (s *Store) OAuthRefreshToken(refreshTokenHash string) (OAuthToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	accessHash, exists := s.oauthRefresh[refreshTokenHash]
	if !exists {
		return OAuthToken{}, ErrOAuthTokenNotFound
	}
	token, exists := s.oauthTokens[accessHash]
	if !exists {
		return OAuthToken{}, ErrOAuthTokenNotFound
	}
	return cloneOAuthToken(token), nil
}

func (s *Store) UpdateOAuthToken(token OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.oauthTokens[token.TokenHash]; !exists {
		return ErrOAuthTokenNotFound
	}
	s.oauthTokens[token.TokenHash] = cloneOAuthToken(token)
	if token.RefreshTokenHash != "" {
		s.oauthRefresh[token.RefreshTokenHash] = token.TokenHash
	}
	return nil
}

func (s *Store) OAuthConnections(userID, orgID string) ([]OAuthConnection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	connections := make(map[string]OAuthConnection)
	for _, token := range s.oauthTokens {
		if token.UserID != userID || token.OrgID != orgID || token.RevokedAt != nil {
			continue
		}
		client, ok := s.oauthClients[token.ClientID]
		if !ok {
			continue
		}
		existing, ok := connections[token.ClientID]
		if !ok || token.CreatedAt.Before(existing.CreatedAt) {
			connections[token.ClientID] = OAuthConnection{ClientID: token.ClientID, Name: client.Name, Scopes: append([]string(nil), token.Scopes...), CreatedAt: token.CreatedAt}
		}
	}
	result := make([]OAuthConnection, 0, len(connections))
	for _, connection := range connections {
		result = append(result, connection)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Store) OAuthClientConnections(clientID string) ([]OAuthClientConnection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	connections := make(map[string]OAuthClientConnection)
	for _, token := range s.oauthTokens {
		if token.ClientID != clientID || token.RevokedAt != nil {
			continue
		}
		user, userOK := s.users[token.UserID]
		org, orgOK := s.organizations[token.OrgID]
		if !userOK || !orgOK {
			continue
		}
		key := token.UserID + "\x00" + token.OrgID
		existing, ok := connections[key]
		if !ok || token.CreatedAt.Before(existing.CreatedAt) {
			connections[key] = OAuthClientConnection{
				ClientID:  token.ClientID,
				UserID:    token.UserID,
				UserEmail: user.Email,
				OrgID:     token.OrgID,
				OrgName:   org.Name,
				Scopes:    append([]string(nil), token.Scopes...),
				CreatedAt: token.CreatedAt,
			}
		}
	}
	result := make([]OAuthClientConnection, 0, len(connections))
	for _, connection := range connections {
		result = append(result, connection)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OrgName == result[j].OrgName {
			return result[i].UserEmail < result[j].UserEmail
		}
		return result[i].OrgName < result[j].OrgName
	})
	return result, nil
}

func (s *Store) RevokeOAuthClientTokens(userID, orgID, clientID string, revokedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, token := range s.oauthTokens {
		if token.UserID != userID || token.OrgID != orgID || token.ClientID != clientID || token.RevokedAt != nil {
			continue
		}
		token.RevokedAt = &revokedAt
		s.oauthTokens[hash] = cloneOAuthToken(token)
	}
	return nil
}

func (s *Store) RevokeAllOAuthClientTokens(clientID string, revokedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, token := range s.oauthTokens {
		if token.ClientID != clientID || token.RevokedAt != nil {
			continue
		}
		token.RevokedAt = &revokedAt
		s.oauthTokens[hash] = cloneOAuthToken(token)
	}
	return nil
}

func (s *Store) CreateApp(app App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[app.ID]; exists {
		return ErrAppExists
	}
	for _, existing := range s.apps {
		if existing.Hostname == app.Hostname {
			return ErrAppExists
		}
	}
	s.apps[app.ID] = app
	s.capabilityToApp[app.RuntimeToken] = app.ID
	return nil
}

func (s *Store) CountAppsByOrg(orgID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, app := range s.apps {
		if app.OrgID == orgID {
			count++
		}
	}
	return count, nil
}

func (s *Store) ListApps() ([]App, error) {
	return s.ListAppsByOrg("")
}

func (s *Store) ListAppsByOrg(orgID string) ([]App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	apps := make([]App, 0, len(s.apps))
	for _, app := range s.apps {
		if orgID != "" && app.OrgID != orgID {
			continue
		}
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].ID < apps[j].ID })
	return apps, nil
}

func (s *Store) UpdateApp(app App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, exists := s.apps[app.ID]
	if !exists {
		return ErrAppNotFound
	}
	for _, candidate := range s.apps {
		if candidate.ID != app.ID && candidate.Hostname == app.Hostname {
			return ErrAppExists
		}
	}
	app.RuntimeToken = existing.RuntimeToken
	app.CreatedAt = existing.CreatedAt
	s.apps[app.ID] = app
	return nil
}

func (s *Store) DeleteApp(appID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, exists := s.apps[appID]
	if !exists {
		return ErrAppNotFound
	}
	delete(s.apps, appID)
	delete(s.secrets, appID)
	delete(s.deployments, appID)
	delete(s.active, appID)
	delete(s.capabilityToApp, app.RuntimeToken)
	return nil
}

func (s *Store) CreateKVNamespace(namespace KVNamespace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.kvNamespaces[namespace.ID]; exists {
		return ErrKVNamespaceExists
	}
	for _, existing := range s.kvNamespaces {
		if existing.Name == namespace.Name && existing.OrgID == namespace.OrgID {
			return ErrKVNamespaceExists
		}
	}
	s.kvNamespaces[namespace.ID] = namespace
	return nil
}

func (s *Store) CountKVNamespacesByOrg(orgID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, namespace := range s.kvNamespaces {
		if namespace.OrgID == orgID {
			count++
		}
	}
	return count, nil
}

func (s *Store) ListKVNamespaces() ([]KVNamespace, error) {
	return s.ListKVNamespacesByOrg("")
}

func (s *Store) ListKVNamespacesByOrg(orgID string) ([]KVNamespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	namespaces := make([]KVNamespace, 0, len(s.kvNamespaces))
	for _, namespace := range s.kvNamespaces {
		if orgID != "" && namespace.OrgID != orgID {
			continue
		}
		namespaces = append(namespaces, namespace)
	}
	sort.Slice(namespaces, func(i, j int) bool { return namespaces[i].Name < namespaces[j].Name })
	return namespaces, nil
}

func (s *Store) GetKVNamespace(namespaceID string) (KVNamespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	namespace, ok := s.kvNamespaces[namespaceID]
	if !ok {
		return KVNamespace{}, ErrKVNamespaceNotFound
	}
	return namespace, nil
}

func (s *Store) UpdateKVNamespace(namespace KVNamespace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.kvNamespaces[namespace.ID]
	if !ok {
		return ErrKVNamespaceNotFound
	}
	for _, candidate := range s.kvNamespaces {
		if candidate.ID != namespace.ID && candidate.Name == namespace.Name && candidate.OrgID == namespace.OrgID {
			return ErrKVNamespaceExists
		}
	}
	namespace.OrgID = existing.OrgID
	namespace.CreatedAt = existing.CreatedAt
	s.kvNamespaces[namespace.ID] = namespace
	return nil
}

func (s *Store) DeleteKVNamespace(namespaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	for _, deployments := range s.deployments {
		for _, deployment := range deployments {
			for _, binding := range deployment.KVNamespaces {
				if binding.ID == namespaceID {
					return ErrKVNamespaceInUse
				}
			}
		}
	}
	delete(s.kvNamespaces, namespaceID)
	delete(s.kv, namespaceID)
	delete(s.kvMetrics, namespaceID)
	return nil
}

func (s *Store) CreateObjectStorageBucket(bucket ObjectStorageBucket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.objectBuckets[bucket.ID]; exists {
		return ErrObjectStorageBucketExists
	}
	for _, existing := range s.objectBuckets {
		if existing.Name == bucket.Name && existing.OrgID == bucket.OrgID {
			return ErrObjectStorageBucketExists
		}
	}
	s.objectBuckets[bucket.ID] = bucket
	return nil
}

func (s *Store) CountObjectStorageBucketsByOrg(orgID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, bucket := range s.objectBuckets {
		if bucket.OrgID == orgID {
			count++
		}
	}
	return count, nil
}

func (s *Store) ListObjectStorageBuckets() ([]ObjectStorageBucket, error) {
	return s.ListObjectStorageBucketsByOrg("")
}

func (s *Store) ListObjectStorageBucketsByOrg(orgID string) ([]ObjectStorageBucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	buckets := make([]ObjectStorageBucket, 0, len(s.objectBuckets))
	for _, bucket := range s.objectBuckets {
		if orgID != "" && bucket.OrgID != orgID {
			continue
		}
		buckets = append(buckets, bucket)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
	return buckets, nil
}

func (s *Store) GetObjectStorageBucket(bucketID string) (ObjectStorageBucket, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket, ok := s.objectBuckets[bucketID]
	if !ok {
		return ObjectStorageBucket{}, ErrObjectStorageBucketNotFound
	}
	return bucket, nil
}

func (s *Store) UpdateObjectStorageBucket(bucket ObjectStorageBucket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.objectBuckets[bucket.ID]
	if !ok {
		return ErrObjectStorageBucketNotFound
	}
	for _, candidate := range s.objectBuckets {
		if candidate.ID != bucket.ID && candidate.Name == bucket.Name && candidate.OrgID == bucket.OrgID {
			return ErrObjectStorageBucketExists
		}
	}
	bucket.OrgID = existing.OrgID
	bucket.CreatedAt = existing.CreatedAt
	s.objectBuckets[bucket.ID] = bucket
	return nil
}

func (s *Store) DeleteObjectStorageBucket(bucketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objectBuckets[bucketID]; !ok {
		return ErrObjectStorageBucketNotFound
	}
	for _, deployments := range s.deployments {
		for _, deployment := range deployments {
			for _, binding := range deployment.ObjectStorageBuckets {
				if binding.BucketID == bucketID {
					return ErrObjectStorageBucketInUse
				}
			}
		}
	}
	delete(s.objectBuckets, bucketID)
	delete(s.objectMetrics, bucketID)
	return nil
}

func (s *Store) ListSecrets(appID string) ([]SecretRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.apps[appID]; !exists {
		return nil, ErrAppNotFound
	}
	records := make([]SecretRecord, 0, len(s.secrets[appID]))
	for _, secret := range s.secrets[appID] {
		copy := secret
		copy.Nonce = append([]byte(nil), secret.Nonce...)
		copy.Ciphertext = append([]byte(nil), secret.Ciphertext...)
		records = append(records, copy)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

func (s *Store) PutSecret(appID string, secret SecretRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[appID]; !exists {
		return ErrAppNotFound
	}
	if s.secrets[appID] == nil {
		s.secrets[appID] = make(map[string]SecretRecord)
	}
	copy := secret
	copy.Nonce = append([]byte(nil), secret.Nonce...)
	copy.Ciphertext = append([]byte(nil), secret.Ciphertext...)
	s.secrets[appID][secret.Name] = copy
	return nil
}

func (s *Store) DeleteSecret(appID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[appID]; !exists {
		return ErrAppNotFound
	}
	if _, exists := s.secrets[appID][name]; !exists {
		return ErrSecretNotFound
	}
	delete(s.secrets[appID], name)
	return nil
}

func (s *Store) NextPort() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	port := 9001
	for _, deployments := range s.deployments {
		for _, deployment := range deployments {
			if deployment.Port >= port {
				port = deployment.Port + 1
			}
		}
	}
	return port, nil
}

func (s *Store) Activate(deployment Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[deployment.AppID]; !exists {
		return ErrAppNotFound
	}
	s.deployments[deployment.AppID] = append(s.deployments[deployment.AppID], deployment)
	s.active[deployment.AppID] = deployment.ID
	return nil
}

func (s *Store) SetActive(appID, deploymentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[appID]; !exists {
		return ErrAppNotFound
	}
	for _, deployment := range s.deployments[appID] {
		if deployment.ID == deploymentID {
			s.active[appID] = deployment.ID
			return nil
		}
	}
	if deploymentID == "" {
		delete(s.active, appID)
		return nil
	}
	return errors.New("deployment not found")
}

func (s *Store) DeleteDeployment(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for appID, deployments := range s.deployments {
		for i, deployment := range deployments {
			if deployment.ID != id {
				continue
			}
			s.deployments[appID] = append(deployments[:i], deployments[i+1:]...)
			if s.active[appID] == id {
				delete(s.active, appID)
			}
			return nil
		}
	}
	return errors.New("deployment not found")
}

func (s *Store) ActiveDeployments() ([]ActiveDeployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := make([]ActiveDeployment, 0, len(s.active))
	for appID, deploymentID := range s.active {
		for _, deployment := range s.deployments[appID] {
			if deployment.ID == deploymentID {
				active = append(active, ActiveDeployment{App: s.apps[appID], Deployment: deployment})
				break
			}
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].App.ID < active[j].App.ID })
	return active, nil
}

func (s *Store) ListDeployments() ([]DeploymentRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var records []DeploymentRecord
	for appID, deployments := range s.deployments {
		for _, deployment := range deployments {
			records = append(records, DeploymentRecord{
				App:        s.apps[appID],
				Deployment: deployment,
				Active:     deployment.ID == s.active[appID],
			})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Deployment.CreatedAt.After(records[j].Deployment.CreatedAt)
	})
	return records, nil
}

func (s *Store) AppIDForCapability(capability string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	appID, ok := s.capabilityToApp[capability]
	if !ok {
		return "", ErrInvalidCapability
	}
	return appID, nil
}

func (s *Store) KVGet(capability, namespaceID, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.capabilityToApp[capability]; !ok {
		return nil, false, ErrInvalidCapability
	}
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return nil, false, ErrKVNamespaceNotFound
	}
	value, ok := s.kv[namespaceID][key]
	return append([]byte(nil), value...), ok, nil
}

func (s *Store) KVList(capability, namespaceID string) ([]WorkerKVKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.capabilityToApp[capability]; !ok {
		return nil, ErrInvalidCapability
	}
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return nil, ErrKVNamespaceNotFound
	}
	items := make([]WorkerKVKey, 0, len(s.kv[namespaceID]))
	for key, value := range s.kv[namespaceID] {
		items = append(items, WorkerKVKey{Key: key, Size: int64(len(value))})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (s *Store) KVPut(capability, namespaceID, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.capabilityToApp[capability]; !ok {
		return ErrInvalidCapability
	}
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	if s.kv[namespaceID] == nil {
		s.kv[namespaceID] = make(map[string][]byte)
	}
	s.kv[namespaceID][key] = append([]byte(nil), value...)
	return nil
}

func (s *Store) KVDelete(capability, namespaceID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.capabilityToApp[capability]; !ok {
		return ErrInvalidCapability
	}
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	delete(s.kv[namespaceID], key)
	return nil
}

func (s *Store) KVNamespaceMetrics(namespaceID string) (KVNamespaceMetrics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return KVNamespaceMetrics{}, ErrKVNamespaceNotFound
	}
	metrics := s.kvMetrics[namespaceID]
	metrics.Available = true
	return metrics, nil
}

func (s *Store) IncrementKVNamespaceReads(namespaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	metrics := s.kvMetrics[namespaceID]
	metrics.Reads++
	s.kvMetrics[namespaceID] = metrics
	return nil
}

func (s *Store) IncrementKVNamespaceWrites(namespaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	metrics := s.kvMetrics[namespaceID]
	metrics.Writes++
	s.kvMetrics[namespaceID] = metrics
	return nil
}

func (s *Store) AdjustKVNamespaceSize(namespaceID string, delta int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	metrics := s.kvMetrics[namespaceID]
	metrics.Size += delta
	if metrics.Size < 0 {
		metrics.Size = 0
	}
	s.kvMetrics[namespaceID] = metrics
	return nil
}

func (s *Store) KVStorageBytesByOrg(orgID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for namespaceID, namespace := range s.kvNamespaces {
		if namespace.OrgID != orgID {
			continue
		}
		total += s.kvMetrics[namespaceID].Size
	}
	return total, nil
}

func (s *Store) ObjectStorageBucketMetrics(bucketID string) (ObjectStorageBucketMetrics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.objectBuckets[bucketID]; !ok {
		return ObjectStorageBucketMetrics{}, ErrObjectStorageBucketNotFound
	}
	metrics := s.objectMetrics[bucketID]
	metrics.Available = true
	return metrics, nil
}

func (s *Store) ObjectStorageBytesByOrg(orgID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for bucketID, bucket := range s.objectBuckets {
		if bucket.OrgID != orgID {
			continue
		}
		total += s.objectMetrics[bucketID].Size
	}
	return total, nil
}

func (s *Store) IncrementObjectStorageBucketReads(bucketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objectBuckets[bucketID]; !ok {
		return ErrObjectStorageBucketNotFound
	}
	metrics := s.objectMetrics[bucketID]
	metrics.Reads++
	s.objectMetrics[bucketID] = metrics
	return nil
}

func (s *Store) IncrementObjectStorageBucketWrites(bucketID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objectBuckets[bucketID]; !ok {
		return ErrObjectStorageBucketNotFound
	}
	metrics := s.objectMetrics[bucketID]
	metrics.Writes++
	s.objectMetrics[bucketID] = metrics
	return nil
}

func (s *Store) AdjustObjectStorageBucketSize(bucketID string, delta int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objectBuckets[bucketID]; !ok {
		return ErrObjectStorageBucketNotFound
	}
	metrics := s.objectMetrics[bucketID]
	metrics.Size += delta
	if metrics.Size < 0 {
		metrics.Size = 0
	}
	s.objectMetrics[bucketID] = metrics
	return nil
}

func cloneOAuthClient(client OAuthClient) OAuthClient {
	client.RedirectURIs = append([]string(nil), client.RedirectURIs...)
	client.Scopes = append([]string(nil), client.Scopes...)
	client.SecretHash = append([]byte(nil), client.SecretHash...)
	return client
}

func cloneOAuthAuthorizationCode(code OAuthAuthorizationCode) OAuthAuthorizationCode {
	code.Scopes = append([]string(nil), code.Scopes...)
	if code.UsedAt != nil {
		used := *code.UsedAt
		code.UsedAt = &used
	}
	return code
}

func cloneOAuthToken(token OAuthToken) OAuthToken {
	token.Scopes = append([]string(nil), token.Scopes...)
	if token.RevokedAt != nil {
		revoked := *token.RevokedAt
		token.RevokedAt = &revoked
	}
	return token
}

func cloneOrganizationInvite(invite OrganizationInvite) OrganizationInvite {
	invite.Scopes = append([]string(nil), invite.Scopes...)
	if invite.AcceptedAt != nil {
		accepted := *invite.AcceptedAt
		invite.AcceptedAt = &accepted
	}
	if invite.RevokedAt != nil {
		revoked := *invite.RevokedAt
		invite.RevokedAt = &revoked
	}
	return invite
}
