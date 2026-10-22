package nanoflare

import "context"

// ContextObjectStore lets runtime requests carry cancellation and invocation
// deadlines into actual storage I/O. Existing synchronous fixture stores remain
// compatible; production MinIO implements all of these operations.
type ContextObjectStore interface {
	PutContext(context.Context, string, string, string, []byte) (ObjectInfo, error)
	GetContext(context.Context, string, string) (ObjectBody, error)
	HeadContext(context.Context, string, string) (ObjectInfo, error)
	ListContext(context.Context, string, string) ([]ObjectInfo, error)
	DeleteContext(context.Context, string, string) error
}

func (s *Service) objectGetContext(ctx context.Context, app, path string) (ObjectBody, error) {
	if err := ctx.Err(); err != nil {
		return ObjectBody{}, err
	}
	if store, ok := s.objects.(ContextObjectStore); ok {
		return store.GetContext(ctx, app, path)
	}
	return s.objects.Get(app, path)
}
func (s *Service) objectHeadContext(ctx context.Context, app, path string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	if store, ok := s.objects.(ContextObjectStore); ok {
		return store.HeadContext(ctx, app, path)
	}
	return s.objects.Head(app, path)
}
func (s *Service) objectPutContext(ctx context.Context, app, path, contentType string, data []byte) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	if store, ok := s.objects.(ContextObjectStore); ok {
		return store.PutContext(ctx, app, path, contentType, data)
	}
	return s.objects.Put(app, path, contentType, data)
}
func (s *Service) objectDeleteContext(ctx context.Context, app, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store, ok := s.objects.(ContextObjectStore); ok {
		return store.DeleteContext(ctx, app, path)
	}
	return s.objects.Delete(app, path)
}
