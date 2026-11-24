# Object storage quotas and direct transfers

Set a bucket's optional quota with the organization-scoped credential that provisions the bucket. It needs the `objects:write` scope.

```http
PATCH /v1/organizations/{orgID}/object-storage-buckets/{bucketID}
Content-Type: application/json

{"max_bytes":10000000000}
```

The PATCH and bucket reads return the configured `max_bytes`. The quota uses bytes, so this example sets 10 decimal GB. A quota can be set below existing usage. The bucket can still be read, deleted from, and overwritten without increasing its stored bytes. Any organization-wide cap also applies.

Worker binding `put` calls in dynamic dispatch still have a 10 MiB request limit. Direct single-part uploads declare the exact size and can be up to 64 MiB. Use multipart for larger objects. Multipart uploads declare each part's size and can contain up to 10,000 parts. Each part can be up to 5 GiB, and every non-final part must be at least 5 MiB. Nanoflare caps a multipart object at 5 TiB, subject to the provider and the bucket and organization quotas. Any lower limit on the public endpoint still applies.

## Configure the storage endpoint

- Set `MINIO_PUBLIC_ENDPOINT` to the browser-reachable HTTP(S) origin of the S3 API. Set `MINIO_REGION` if the S3 endpoint uses a region other than `us-east-1`. Use HTTPS for HTTPS Sites. Forward the original host and request path when proxying this endpoint.
- Signed uploads require PostgreSQL and a separate MinIO identity configured through `MINIO_UPLOAD_ACCESS_KEY` and `MINIO_UPLOAD_SECRET_KEY`. Grant that identity only `s3:PutObject` for `arn:aws:s3:::BUCKET/apps/object-storage-buckets/staged-uploads/*` and `arn:aws:s3:::BUCKET/apps/object-storage-buckets/buckets/*`, where `BUCKET` is `MINIO_BUCKET`. The pinned MinIO release authorizes UploadPart with the same `s3:PutObject` action as a normal PUT. A leaked signing credential could therefore write directly to final keys and bypass Nanoflare quotas. Keep it in the server secret store, restrict access and rotate it if exposed. Give this identity no source read permission and keep the bucket private. The daemon's internal identity creates and completes multipart uploads and publishes verified objects.
- Configure MinIO CORS for the consuming browser origins. Allow GET, HEAD, and PUT, and include `If-None-Match` and `Content-Type` in allowed request headers. Check the CORS response on the public endpoint including any reverse proxy.
- Configure staging object expiry and incomplete multipart abort as backstops for interrupted requests. Apply object expiry only to the `apps/object-storage-buckets/staged-uploads/` prefix. Leave staging objects for at least six hours and keep incomplete multipart uploads long enough for the largest transfers you support. If storage versioning is enabled, expire noncurrent staging versions too.
- The legacy internal endpoint that issued direct PUTs to the final key returns HTTP 410 after upgrade. Upgrade all Nanoflare instances, then drain previously issued URLs and their in-flight PUTs or revoke the signing credential used by those URLs before enabling bucket quotas.

## Worker binding

The same binding that can read or write the bucket can create transfer URLs for it. Return a URL to the browser only after applying the Site's own authorization to the requested key.

- `await env.OBJECTS.presignDownload(key)` returns a GET URL valid for five minutes.
- `await env.OBJECTS.createUpload(key, size)` returns `{ id, url, expires_at }`. PUT a fixed-length body such as a `Blob` to `url` with the header `If-None-Match: *`. The browser supplies the length from the body. Call `await env.OBJECTS.completeUpload(id)` after the PUT succeeds. Single-part URLs allow one hour to upload and an additional hour to complete after URL expiry.
- `await env.OBJECTS.createMultipartUpload(key, partSizes)` returns a session ID and expiry. Use `await env.OBJECTS.signUploadPart(id, partNumber)` for each one-based part number and PUT that part to its URL. Each signing request renews the session for 24 hours, and completion can run for an additional hour after URL expiry. Call `await env.OBJECTS.completeUpload(id)` after uploading every part. Nanoflare reads the part sizes and ETags directly from storage before completing the upload.
- `await env.OBJECTS.abortUpload(id)` aborts an unfinished session. The background cleanup removes staging data and releases reservations for expired sessions. A repeated completion returns the recorded object metadata while the session is retained.

Only completion publishes the key. Single-part uploads target staging and discard the staged payload after completion or abort. Multipart part URLs authorize a specific part of a pending upload at the final key. Worker writes to different keys can run concurrently. A write or delete for a key with an active upload or another mutation in progress returns HTTP 409. The quota includes committed bytes and positive growth reserved by active sessions. In-flight uploads also consume a separate staging budget based on the configured byte limit. An existing object above a lowered quota can still be replaced without growth. The daemon rebuilds usage for existing and interrupted buckets in the background. Quota-checked growth returns HTTP 503 while the bucket or its organization is waiting for a rebuild. The metrics view can show the last recorded usage during that interval.

An admitted buffered PUT can finish after a client disconnect. If its response is lost, Nanoflare checks the object with a separate timeout. A confirmed commit or definitive rejection releases the key immediately. An ambiguous provider outcome retains its reservation until recovery to guard against a late commit.
