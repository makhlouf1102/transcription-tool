# Local transcription API

The API lets local scripts and agents submit media, poll progress, and retrieve transcripts. It is available at `http://127.0.0.1:8766` while the transcription app is running. The caller must run on the same machine as the app because the server listens on loopback only. The app uses the transcription provider and settings selected in its UI. Local Whisper does not need an API key.

An OpenAPI 3.1 description is available at [`/openapi.json`](http://127.0.0.1:8766/openapi.json) when the app is running.

## Submit a transcription

Send the media file as the raw request body. `X-Filename` is optional; when provided, give the transcript a descriptive name with the optional `X-Name` header. If `X-Name` is omitted, the filename without its extension is used. URL-encode non-ASCII names in headers. Model, language, and prompt headers are optional; omitted values use the saved app settings.

```sh
curl -X POST http://127.0.0.1:8766/api/v1/transcriptions \
  -H 'Content-Type: application/octet-stream' \
  -H 'X-Filename: lecture.mp4' \
  -H 'X-Name: GTI611%20lecture%201' \
  --data-binary @lecture.mp4
```

Response (`202 Accepted`):

```json
{"id":"0123456789abcdef0123456789abcdef","name":"GTI611 lecture 1"}
```

Optional headers:

- `X-Model`: OpenAI `gpt-4o-transcribe-diarize`, `gpt-4o-transcribe`, or `whisper-1`; AssemblyAI `assemblyai-universal-3-5-pro`, `assemblyai-universal-2`, `assemblyai-universal-3-5-pro-diarize`, or `assemblyai-universal-2-diarize`; Local Whisper `whisper.cpp-base`. The model must belong to the currently selected provider in Settings.
- `X-Name`: transcript label (up to 200 characters); defaults to the filename stem
- `X-Language`: language code such as `en` or `fr`
- `X-Prompt`: vocabulary or context hint (up to 4,000 characters)

## Poll and retrieve results

Poll the job until its `status` is `complete`, `error`, or `interrupted`:

```sh
curl http://127.0.0.1:8766/api/v1/transcriptions/0123456789abcdef0123456789abcdef
```

The response includes `id`, `name`, `status`, `progress`, `message`, `current_part`, `total_parts`, `duration`, `filename`, `file_size`, `model`, and `audio_available`. The ID uniquely identifies a transcription even when multiple transcriptions use the same name. Add `?detail=1` to include transcript text in the JSON response.

Retrieve the transcript as plain UTF-8 text. The response also has `X-Transcription-ID` and URL-encoded `X-Transcription-Name` headers, so the raw transcript body stays free of metadata:

```sh
curl http://127.0.0.1:8766/api/v1/transcriptions/0123456789abcdef0123456789abcdef/transcript
```

List up to the 100 most recent jobs:

```sh
curl http://127.0.0.1:8766/api/v1/transcriptions
```

If the converted MP3 is available, it can be streamed or downloaded from:

```text
GET /api/v1/transcriptions/{id}/audio
```

The audio endpoint supports HTTP byte ranges for seeking. It returns `404` until the MP3 is ready or if no MP3 was retained for that job.

Delete a finished transcription and its saved audio with `DELETE`. Active jobs cannot be deleted while they are processing:

```sh
curl -X DELETE http://127.0.0.1:8766/api/v1/transcriptions/0123456789abcdef0123456789abcdef
```

## Limits and errors

- Uploads are limited to 20 GB.
- OpenAI and AssemblyAI require a saved API key. Local Whisper runs on the computer and requires no API key.
- Transcription runs in a background worker; callers should poll instead of holding the upload request open for the full transcription.
- Errors are JSON objects with an `error` field, except successful transcript downloads, which are `text/plain`.

Legacy `/api/jobs` routes remain available alongside `/api/v1/transcriptions`.
