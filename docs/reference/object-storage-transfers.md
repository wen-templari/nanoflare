# Object storage quotas and direct transfers

Set a bucket's optional quota with the organization-scoped credential that provisions the bucket. It needs the `objects:write` scope.

```http
PATCH /v1/organizations/{orgID}/object-storage-buckets/{bucketID}
Content-Type: application/json

{"max_bytes":10000000000}
```

The PATCH and bucket reads return the configured `max_bytes`. The quota uses bytes, so this example sets 10 decimal GB. A quota can be set below existing usage. The bucket can still be read, deleted from, and overwritten without increasing its stored bytes. Any organization-wide cap also applies.

Worker binding `put` calls in dynamic dispatch still have a 10 MiB request limit. Direct single-part uploads declare the exact size and can be up to 5 GiB. Multipart uploads declare each part's size and can contain up to 10,000 parts. Each part can be up to 5 GiB, and every non-final part must be at least 5 MiB. Nanoflare caps a multipart object at 5 TiB, subject to the provider and the bucket and organization quotas. The pinned MinIO release accepts PUT requests larger than Nanoflare’s 5 GiB single-part cap. Any lower limit on the public endpoint still applies.

## Configure the storage endpoint

- Set `MINIO_PUBLIC_ENDPOINT` to the browser-reachable HTTP(S) origin of the S3 API. Set `MINIO_REGION` if the S3 endpoint uses a region other than `us-east-1`. Use HTTPS for HTTPS Sites. Forward the original host and request path when proxying this endpoint.
- Signed uploads require PostgreSQL and a separate MinIO identity configured through `MINIO_UPLOAD_ACCESS_KEY` and `MINIO_UPLOAD_SECRET_KEY`. Grant that identity only `s3:PutObject` for `arn:aws:s3:::BUCKET/apps/object-storage-buckets/staged-uploads/*`, where `BUCKET` is `MINIO_BUCKET`. Keep the bucket private. The daemon's internal identity creates and completes multipart uploads and publishes verified objects.
- Configure MinIO CORS for the consuming browser origins. Allow GET, HEAD, and PUT, and include `If-None-Match` and `Content-Type` in allowed request headers. Check the CORS response on the public endpoint including any reverse proxy.
- Configure storage lifecycle expiry and incomplete-multipart abort for the `apps/object-storage-buckets/staged-uploads/` prefix as a backstop for interrupted requests. If storage versioning is enabled, expire noncurrent staging versions too. Leave the lifecycle window longer than the 15-minute upload session and its cleanup grace period.
- The legacy internal endpoint that issued direct PUTs to the final key returns HTTP 410 after upgrade. Upgrade all Nanoflare instances, then drain previously issued URLs and their in-flight PUTs or revoke the signing credential used by those URLs before enabling bucket quotas.

## Worker binding

The same binding that can read or write the bucket can create transfer URLs for it. Return a URL to the browser only after applying the Site's own authorization to the requested key.

- `await env.OBJECTS.presignDownload(key)` returns a GET URL valid for five minutes.
- `await env.OBJECTS.createUpload(key, size)` returns `{ id, url, expires_at }`. PUT a fixed-length body such as a `Blob` to `url` with the header `If-None-Match: *`. The browser supplies the length from the body. Call `await env.OBJECTS.completeUpload(id)` after the PUT succeeds.
- `await env.OBJECTS.createMultipartUpload(key, partSizes)` returns a session ID and expiry. Use `await env.OBJECTS.signUploadPart(id, partNumber)` for each one-based part number and PUT that part to its URL. Call `await env.OBJECTS.completeUpload(id)` after uploading every part. Nanoflare reads the part sizes and ETags directly from storage before completing the upload.
- `await env.OBJECTS.abortUpload(id)` aborts an unfinished session. Expired sessions release reserved quota, and the background cleanup removes staging data after the URLs have expired. A repeated completion returns the recorded object metadata while the session is retained.

Only completion publishes the key. Signed PUTs target staging, and regular Worker PUT and delete calls for a key with an active upload session return HTTP 409. The quota includes committed bytes and positive growth reserved by active sessions.
