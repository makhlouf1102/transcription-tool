import createClient from 'openapi-fetch';
import type { components, paths } from './api-schema';

export const api = createClient<paths>({ baseUrl: '' });

type ApiResult<T> = {
  data?: T;
  error?: unknown;
  response: Response;
};

function readError(error: unknown): string | undefined {
  if (!error || typeof error !== 'object' || !('error' in error)) return undefined;
  const message = error.error;
  return typeof message === 'string' ? message : undefined;
}

export async function apiData<T>(resultPromise: Promise<ApiResult<T>>, fallback: string): Promise<T> {
  const result = await resultPromise;
  if (!result.response.ok) throw new Error(readError(result.error) || fallback);
  if (result.data === undefined) throw new Error(fallback);
  return result.data;
}

export function transcriptionAudioURL(id: string): string {
  return `/api/v1/transcriptions/${encodeURIComponent(id)}/audio`;
}

export function uploadTranscription(
  file: Blob,
  headers: Record<string, string>,
  onProgress: (percent: number) => void,
): Promise<components['schemas']['CreatedJob']> {
  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open('POST', '/api/v1/transcriptions');
    request.responseType = 'json';
    Object.entries(headers).forEach(([name, value]) => request.setRequestHeader(name, value));
    request.upload.addEventListener('progress', (event) => {
      if (event.lengthComputable && event.total > 0) {
        onProgress(Math.min(100, Math.round((event.loaded / event.total) * 100)));
      }
    });
    request.addEventListener('load', () => {
      const response = request.response as { error?: unknown; id?: unknown; name?: unknown } | null;
      if (request.status < 200 || request.status >= 300) {
        reject(new Error(typeof response?.error === 'string' ? response.error : 'Could not start transcription.'));
      } else if (typeof response?.id !== 'string' || typeof response?.name !== 'string') {
        reject(new Error('Could not start transcription.'));
      } else {
        resolve({ id: response.id, name: response.name });
      }
    });
    request.addEventListener('error', () => reject(new Error('Audio upload failed. Check your connection and try again.')));
    request.addEventListener('abort', () => reject(new Error('Audio upload was canceled.')));
    request.send(file);
  });
}
