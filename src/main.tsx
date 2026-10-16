import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Alert, Badge, Button, MantineProvider, Paper, PasswordInput, Progress, Select, Text, TextInput, Textarea, Title, createTheme } from '@mantine/core';
import { api, apiData, transcriptionAudioURL, uploadTranscription } from './api';
import type { components } from './api-schema';
import '@mantine/core/styles.css';
import '../web/styles.css';

type Config = components['schemas']['Config'];
type Job = components['schemas']['Job'];

const DEFAULT_MODEL = 'gpt-4o-transcribe-diarize';
const LOCAL_MODEL = 'whisper.cpp-base';
const ASSEMBLY_MODEL = 'assemblyai-universal-2';
const ASSEMBLY_PRO_MODEL = 'assemblyai-universal-3-5-pro';
const ASSEMBLY_DIARIZED_MODEL = 'assemblyai-universal-2-diarize';
const ASSEMBLY_PRO_DIARIZED_MODEL = 'assemblyai-universal-3-5-pro-diarize';
const MODEL_COST_PER_MINUTE = { 'gpt-4o-transcribe': 0.006, 'whisper-1': 0.006, [DEFAULT_MODEL]: null };
const ASSEMBLYAI_MODEL_COST_PER_HOUR = { [ASSEMBLY_MODEL]: 0.15, [ASSEMBLY_PRO_MODEL]: 0.21, [ASSEMBLY_DIARIZED_MODEL]: 0.17, [ASSEMBLY_PRO_DIARIZED_MODEL]: 0.23 };
const ASSEMBLYAI_PRICING_URL = 'https://www.assemblyai.com/pricing/';
const ASSEMBLYAI_MODELS = [
  [ASSEMBLY_PRO_MODEL, 'Universal-3.5 Pro · $0.21/hr'],
  [ASSEMBLY_PRO_DIARIZED_MODEL, 'Universal-3.5 Pro + speaker labels · $0.23/hr'],
  [ASSEMBLY_MODEL, 'Universal-2 · $0.15/hr'],
  [ASSEMBLY_DIARIZED_MODEL, 'Universal-2 + speaker labels · $0.17/hr'],
];
const MODELS = [
  [DEFAULT_MODEL, 'GPT-4o · Speaker diarization'],
  ['gpt-4o-transcribe', 'GPT-4o · Highest accuracy, no labels'],
  ['whisper-1', 'Whisper · Classic'],
];
const LOCAL_MODELS = [[LOCAL_MODEL, 'Whisper.cpp · Base']];
const LANGUAGES = [
  ['auto', 'Auto-detect'], ['en', 'English'], ['fr', 'French'], ['es', 'Spanish'], ['de', 'German'],
  ['it', 'Italian'], ['pt', 'Portuguese'], ['ja', 'Japanese'], ['zh', 'Chinese'], ['ko', 'Korean'],
  ['ar', 'Arabic'], ['hi', 'Hindi'], ['ru', 'Russian'], ['nl', 'Dutch'], ['sv', 'Swedish'],
];
const theme = createTheme({
  primaryColor: 'green',
  colors: { green: ['#eff4eb', '#e3ebdc', '#cfddc6', '#afc59f', '#91ad7e', '#719064', '#5b7b53', '#486743', '#375535', '#284c39'] },
  fontFamily: 'DM Sans, sans-serif',
  headings: { fontFamily: 'Manrope, sans-serif' },
  defaultRadius: 'md',
});

function prettySize(bytes = 0) {
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
}

function baseName(filename = '') {
  return filename.replace(/\.[^.]+$/, '') || filename;
}

function formatDuration(seconds) {
  const totalMinutes = Math.max(1, Math.ceil(seconds / 60));
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return hours ? `${hours} hr${hours === 1 ? '' : 's'}${minutes ? ` ${minutes} min` : ''}` : `${totalMinutes} min`;
}

function dateLabel(timestamp) {
  if (!timestamp) return '—';
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(timestamp * 1000));
}

function statusLabel(status) {
  return ({ processing: 'In progress', queued: 'Waiting', complete: 'Complete', error: 'Failed', interrupted: 'Interrupted' })[status] || status;
}

function statusColor(status) {
  return ({ processing: 'yellow', queued: 'yellow', complete: 'green', error: 'red', interrupted: 'red' })[status] || 'gray';
}

function jobErrorCopy(job) {
  const message = job?.message || statusLabel(job?.status);
  if (message.includes('Could not read this media file.')) {
    return {
      title: 'We couldn’t open this media file.',
      detail: 'Try a different audio or video file. If you run this Clearword install, check that FFmpeg and FFprobe are installed.',
    };
  }
  return { title: message, detail: '' };
}

function modelLabel(model) {
  if (model === LOCAL_MODEL) return 'Whisper.cpp · Base (local)';
  if (model === ASSEMBLY_PRO_DIARIZED_MODEL) return 'AssemblyAI · Universal-3.5 Pro with speaker labels';
  if (model === ASSEMBLY_DIARIZED_MODEL) return 'AssemblyAI · Universal-2 with speaker labels';
  if (model === ASSEMBLY_PRO_MODEL) return 'AssemblyAI · Universal-3.5 Pro';
  if (model === ASSEMBLY_MODEL) return 'AssemblyAI · Universal-2';
  return Object.fromEntries(MODELS)[model] || model || 'Unknown model';
}

function modelForProvider(provider, model) {
  if (provider === 'local') return LOCAL_MODELS.some(([modelId]) => modelId === model) ? model : LOCAL_MODEL;
  if (provider === 'assemblyai') return ASSEMBLYAI_MODELS.some(([modelId]) => modelId === model) ? model : ASSEMBLY_MODEL;
  return ASSEMBLYAI_MODELS.some(([modelId]) => modelId === model) || !MODELS.some(([modelId]) => modelId === model) ? DEFAULT_MODEL : model;
}

function assemblyModelDescription(model) {
  if (model === ASSEMBLY_PRO_DIARIZED_MODEL) return 'Highest accuracy · speaker labels · $0.23 per audio hour.';
  if (model === ASSEMBLY_DIARIZED_MODEL) return 'Broad language coverage · speaker labels · $0.17 per audio hour.';
  if (model === ASSEMBLY_PRO_MODEL) return 'Highest accuracy · 18 languages · $0.21 per audio hour.';
  return 'Broad language coverage · 99+ languages · $0.15 per audio hour.';
}

function languageLabel(language) {
  return Object.fromEntries(LANGUAGES)[language || 'auto'] || language || 'Auto-detect';
}

function keyValidationError(message, status = 'unavailable') {
  return Object.assign(new Error(message), { keyValidation: true, keyStatus: status });
}

function Brand() {
  return <a className="brand" href="/" aria-label="Clearword home"><span className="brand-mark"><i /><i /><i /></span><span>clearword</span></a>;
}

function UploadIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 16V4m0 0L7.5 8.5M12 4l4.5 4.5M5 14.5v3A2.5 2.5 0 0 0 7.5 20h9a2.5 2.5 0 0 0 2.5-2.5v-3" /></svg>;
}

function PlusIcon() {
  return <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M10 4v12M4 10h12" /></svg>;
}

function App() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [selectedJobId, setSelectedJobId] = useState(null);
  const [view, setView] = useState('new');
  const [file, setFile] = useState(null);
  const [fileName, setFileName] = useState('');
  const [audioDuration, setAudioDuration] = useState(null);
  const [settings, setSettings] = useState<Config>({ provider: 'openai', api_key: '', assemblyai_api_key: '', model: DEFAULT_MODEL, language: '', prompt: '' });
  const [settingsReady, setSettingsReady] = useState(false);
  const [settingsTouched, setSettingsTouched] = useState(false);
  const [configStatus, setConfigStatus] = useState('Loading saved settings…');
  const [keyCheck, setKeyCheck] = useState({ status: 'unchecked', message: '' });
  const [validatingKey, setValidatingKey] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [uploadProgress, setUploadProgress] = useState<number | null>(null);
  const [retryingJobId, setRetryingJobId] = useState(null);
  const [deletingJobId, setDeletingJobId] = useState(null);
  const [error, setError] = useState('');
  const [globalError, setGlobalError] = useState('');
  const [copyLabel, setCopyLabel] = useState('Copy text');
  const [durationUnavailable, setDurationUnavailable] = useState(false);
  const [historyScrollable, setHistoryScrollable] = useState(false);
  const settingsTimer = useRef(null);
  const fileInput = useRef(null);
  const jobsList = useRef<HTMLDivElement>(null);

  const selectedJob = jobs.find((job) => job.id === selectedJobId) || null;
  const orderedJobs = useMemo(() => [...jobs].sort((a, b) => (b.created || 0) - (a.created || 0)), [jobs]);
  const isDiarize = settings.model === DEFAULT_MODEL;
  const activeAPIKey = settings.provider === 'assemblyai' ? settings.assemblyai_api_key : settings.provider === 'local' ? '' : settings.api_key;
  const availableModels = settings.provider === 'local' ? LOCAL_MODELS : settings.provider === 'assemblyai' ? ASSEMBLYAI_MODELS : MODELS;
  const readyToSubmit = settingsReady && Boolean(file) && (settings.provider === 'local' || Boolean(activeAPIKey.trim())) && !uploading && !validatingKey && !retryingJobId;

  const updateJob = useCallback((incoming) => {
    setJobs((current) => {
      const index = current.findIndex((job) => job.id === incoming.id);
      if (index < 0) return [incoming, ...current];
      const next = current.slice();
      next[index] = { ...next[index], ...incoming };
      return next;
    });
  }, []);

  const fetchJob = useCallback(async (id, detail = false) => {
    const incoming = await apiData(api.GET('/api/v1/transcriptions/{id}', {
      params: { path: { id }, query: detail ? { detail: '1' } : undefined },
      cache: 'no-store',
    }), 'Could not read transcription.');
    const enriched = { ...incoming, id };
    updateJob(enriched);
    return enriched;
  }, [updateJob]);

  const selectJob = useCallback(async (id) => {
    setSelectedJobId(id);
    setView('job');
    setError('');
    setGlobalError('');
    try {
      await fetchJob(id, true);
    } catch (cause) {
      setGlobalError(cause.message);
    }
  }, [fetchJob]);

  useEffect(() => {
    let cancelled = false;
    apiData(api.GET('/api/config', { cache: 'no-store' }), 'Could not load saved settings.')
      .then((config) => {
        if (cancelled) return;
        const provider = config.provider || 'openai';
        setSettings({ provider, api_key: config.api_key || '', assemblyai_api_key: config.assemblyai_api_key || '', model: modelForProvider(provider, config.model || DEFAULT_MODEL), language: config.language || '', prompt: config.prompt || '' });
        setSettingsReady(true);
        setConfigStatus('Saved locally');
      })
      .catch((cause) => { if (!cancelled) setGlobalError(cause.message); });
    apiData(api.GET('/api/v1/transcriptions', { cache: 'no-store' }), 'Could not load transcription history.')
      .then((data) => {
        if (cancelled) return;
        const initialJobs = data.jobs || [];
        setJobs(initialJobs);
        if (initialJobs.length) {
          setSelectedJobId(initialJobs[0].id);
          setView('job');
          fetchJob(initialJobs[0].id, true).catch((cause) => { if (!cancelled) setGlobalError(cause.message); });
        }
      })
      .catch((cause) => { if (!cancelled) setGlobalError(cause.message); });
    return () => { cancelled = true; };
  }, [fetchJob]);

  useEffect(() => {
    if (!settingsReady || !settingsTouched) return undefined;
    setConfigStatus('Unsaved changes');
    clearTimeout(settingsTimer.current);
    settingsTimer.current = setTimeout(async () => {
      setConfigStatus('Saving…');
      try {
        await apiData(api.POST('/api/config', { body: settings }), 'Could not save settings.');
        setConfigStatus('Saved locally');
      } catch (cause) {
        setConfigStatus('Settings could not be saved');
        setError(cause.message);
      }
    }, 500);
    return () => clearTimeout(settingsTimer.current);
  }, [settings, settingsReady, settingsTouched]);

  useEffect(() => {
    if (!file) {
      setAudioDuration(null);
      setDurationUnavailable(false);
      return undefined;
    }
    let active = true;
    const video = document.createElement('video');
    const objectURL = URL.createObjectURL(file);
    video.preload = 'metadata';
    video.muted = true;
    const timeout = setTimeout(() => {
      if (active) setDurationUnavailable(true);
    }, 12000);
    video.onloadedmetadata = () => {
      clearTimeout(timeout);
      if (active && Number.isFinite(video.duration)) {
        setAudioDuration(video.duration);
        setDurationUnavailable(false);
      }
    };
    video.onerror = () => {
      clearTimeout(timeout);
      if (active) setDurationUnavailable(true);
    };
    video.src = objectURL;
    return () => {
      active = false;
      clearTimeout(timeout);
      video.removeAttribute('src');
      video.load();
      URL.revokeObjectURL(objectURL);
    };
  }, [file]);

  const activeJobIds = jobs.filter((job) => ['processing', 'queued'].includes(job.status)).map((job) => job.id).join(',');
  useEffect(() => {
    const list = jobsList.current;
    if (!list) return undefined;
    const updateScrollHint = () => setHistoryScrollable(list.scrollWidth > list.clientWidth + 1);
    updateScrollHint();
    window.addEventListener('resize', updateScrollHint);
    return () => window.removeEventListener('resize', updateScrollHint);
  }, [orderedJobs.length]);

  useEffect(() => {
    if (!activeJobIds) return undefined;
    let alive = true;
    const timers = activeJobIds.split(',').map((id) => setInterval(async () => {
      try {
        const current = await fetchJob(id, false);
        if (alive && current.status === 'complete' && selectedJobId === id) await fetchJob(id, true);
      } catch (cause) {
        if (!alive) return;
        setJobs((current) => current.map((job) => job.id === id ? { ...job, status: 'error', message: cause.message } : job));
      }
    }, 1400));
    return () => { alive = false; timers.forEach(clearInterval); };
  }, [activeJobIds, fetchJob, selectedJobId]);

  const chooseFile = (next) => {
    if (!next) return;
    setFile(next);
    setFileName(baseName(next.name));
    setError('');
  };

  const openNewTranscription = () => {
    setSelectedJobId(null);
    setView('new');
    setGlobalError('');
    setError('');
    if (window.matchMedia('(max-width: 620px)').matches) {
      document.querySelector('#new-view')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  };

  const chooseAnotherFile = () => {
    openNewTranscription();
    window.requestAnimationFrame(() => fileInput.current?.click());
  };

  const openSettings = () => {
    setView('settings');
    setGlobalError('');
    setError('');
    if (window.matchMedia('(max-width: 620px)').matches) {
      document.querySelector('#settings-view')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  };

  const updateSetting = (key, value) => {
    setSettingsTouched(true);
    setSettings((current) => {
      if (key === 'provider') return { ...current, provider: value, model: modelForProvider(value, current.model) };
      return { ...current, [key]: value };
    });
    if (key === 'api_key' || key === 'assemblyai_api_key' || key === 'provider') setKeyCheck({ status: 'unchecked', message: '' });
  };

  const saveCurrentSettings = async () => {
    clearTimeout(settingsTimer.current);
    setConfigStatus('Saving…');
    await apiData(api.POST('/api/config', { body: settings }), 'Could not save settings.');
    setConfigStatus('Saved locally');
    setSettingsTouched(false);
  };

  const validateSavedAPIKey = async () => {
    if (settings.provider === 'local') {
      setKeyCheck({ status: 'valid', message: 'Local Whisper does not need an API key.' });
      return true;
    }
    if (!activeAPIKey.trim()) {
      const cause = keyValidationError('Add an API key in Settings before validating it.', 'invalid');
      setKeyCheck({ status: cause.keyStatus, message: cause.message });
      throw cause;
    }
    setValidatingKey(true);
    const providerName = settings.provider === 'assemblyai' ? 'AssemblyAI' : 'OpenAI';
    setKeyCheck({ status: 'checking', message: `Checking the saved key with ${providerName}…` });
    try {
      let result: components['schemas']['ValidationResult'];
      try {
        result = await apiData(api.POST('/api/config/validate', { cache: 'no-store' }), 'Could not verify the saved API key.');
      } catch (cause) {
        const status = cause instanceof Error && cause.message.includes('rejected') ? 'invalid' : 'unavailable';
        throw keyValidationError(cause instanceof Error ? cause.message : 'Could not verify the saved API key.', status);
      }
      if (result.valid !== true) throw keyValidationError(`${providerName} returned an unexpected validation response.`);
      setKeyCheck({ status: 'valid', message: `${providerName} accepted this API key.` });
      return true;
    } catch (cause) {
      const failure = cause.keyValidation ? cause : keyValidationError(`Could not reach ${providerName} to verify the key. Check your connection and try again.`);
      setKeyCheck({ status: failure.keyStatus, message: failure.message });
      throw failure;
    } finally {
      setValidatingKey(false);
    }
  };

  const checkAPIKey = async () => {
    setError('');
    if (!activeAPIKey.trim()) {
      const cause = keyValidationError('Add an API key before checking it.', 'invalid');
      setKeyCheck({ status: cause.keyStatus, message: cause.message });
      return;
    }
    try {
      await saveCurrentSettings();
      await validateSavedAPIKey();
    } catch (cause) {
      if (!cause.keyValidation) setError(cause.message || 'Could not save settings.');
    }
  };

  const startTranscription = async ({ audioFile, transcriptionName, model, language, prompt }) => {
    setUploadProgress(0);
    const created = await uploadTranscription(audioFile, {
        'X-Filename': encodeURIComponent(audioFile.name),
        'X-Name': encodeURIComponent(transcriptionName),
        'X-Model': model,
        'X-Language': language,
        'X-Prompt': encodeURIComponent(prompt),
      }, setUploadProgress);
    const job = {
      id: created.id, name: created.name, status: 'processing', progress: 1, message: 'Upload received…',
      transcript: '', filename: audioFile.name, file_size: audioFile.size, model, language, created: Date.now() / 1000,
    };
    updateJob(job);
    setSelectedJobId(job.id);
    setView('job');
    setGlobalError('');
    return job;
  };

  const submitTranscription = async (event) => {
    event.preventDefault();
    if (!file || (settings.provider !== 'local' && !activeAPIKey.trim()) || uploading || validatingKey || retryingJobId) return;
    const currentFile = file;
    const currentSettings = settings;
    const transcriptionName = fileName.trim() || baseName(currentFile.name) || currentFile.name;
    setUploading(true);
    setUploadProgress(null);
    setError('');
    try {
      await saveCurrentSettings();
      if (currentSettings.provider !== 'local') await validateSavedAPIKey();
      await startTranscription({
        audioFile: currentFile,
        transcriptionName,
        model: currentSettings.model,
        language: currentSettings.language,
        prompt: currentSettings.prompt,
      });
      setFile(null);
      setFileName('');
      if (fileInput.current) fileInput.current.value = '';
    } catch (cause) {
      if (cause.keyValidation) {
        setView('settings');
        setGlobalError('');
      } else {
        setError(cause.message);
      }
    } finally {
      setUploading(false);
      setUploadProgress(null);
    }
  };

  const retryTranscription = async (job) => {
    if (!job?.audio_available || retryingJobId || validatingKey || uploading) return;
    setRetryingJobId(job.id);
    setGlobalError('');
    try {
      await saveCurrentSettings();
      if (settings.provider !== 'local') await validateSavedAPIKey();
      const blob = await apiData(api.GET('/api/v1/transcriptions/{id}/audio', {
        params: { path: { id: job.id } }, parseAs: 'blob', cache: 'no-store',
      }), 'Could not load the saved audio. Upload the original file to try again.');
      if (!blob.size) throw new Error('The saved audio is empty. Upload the original file to try again.');
      const filename = `${baseName(job.filename || job.name || 'transcription') || 'transcription'}.mp3`;
      const audioFile = new File([blob], filename, { type: blob.type || 'audio/mpeg' });
      const replacement = await startTranscription({
        audioFile,
        transcriptionName: job.name || baseName(job.filename) || 'Transcription',
        model: job.model || settings.model,
        language: job.language || '',
        prompt: settings.prompt || '',
      });
      try {
        await apiData(api.DELETE('/api/v1/transcriptions/{id}', { params: { path: { id: job.id } } }), 'The retry started, but the previous failed transcription could not be deleted.');
        setJobs((current) => current.filter((item) => item.id !== job.id));
      } catch (cause) {
        setGlobalError(`Retry started${replacement.name ? ` as “${replacement.name}”` : ''}, but the previous failed transcription could not be deleted: ${cause.message}`);
      }
    } catch (cause) {
      if (cause.keyValidation) {
        setView('settings');
        setError('');
        setGlobalError('');
      } else {
        setGlobalError(cause.message || 'Could not retry this transcription.');
      }
    } finally {
      setRetryingJobId(null);
      setUploadProgress(null);
    }
  };

  const deleteTranscription = async (job) => {
    if (!job || ['processing', 'queued'].includes(job.status) || deletingJobId || retryingJobId) return;
    const name = job.name || job.filename || 'this transcription';
    if (!window.confirm(`Delete “${name}”? This also removes its transcript and saved audio.`)) return;
    setDeletingJobId(job.id);
    setGlobalError('');
    try {
      await apiData(api.DELETE('/api/v1/transcriptions/{id}', { params: { path: { id: job.id } } }), 'Could not delete this transcription.');
      setJobs((current) => current.filter((item) => item.id !== job.id));
      if (selectedJobId === job.id) {
        const nextJob = orderedJobs.find((item) => item.id !== job.id);
        if (nextJob) await selectJob(nextJob.id);
        else openNewTranscription();
      }
    } catch (cause) {
      setGlobalError(cause.message || 'Could not delete this transcription.');
    } finally {
      setDeletingJobId(null);
    }
  };

  const removeFile = () => {
    setFile(null);
    setFileName('');
    if (fileInput.current) fileInput.current.value = '';
  };

  const costInfo = useMemo(() => {
    if (!file) return null;
    if (settings.provider === 'local') {
      return { title: 'Local transcription', amount: 'No API charge', detail: 'Whisper.cpp Base · runs on this computer · downloaded once on first start.' };
    }
    if (settings.provider === 'assemblyai') {
      const rate = ASSEMBLYAI_MODEL_COST_PER_HOUR[settings.model];
      if (rate == null) return { title: 'Pricing details', amount: '—', detail: `No published rate for ${modelLabel(settings.model)}.` };
      const unitRate = `$${rate.toFixed(2)} per audio hour`;
      if (!Number.isFinite(audioDuration) || audioDuration <= 0) {
        return {
          title: 'Estimated cost',
          amount: '—',
          detail: `${modelLabel(settings.model)} · ${unitRate} · ${durationUnavailable ? 'Duration unavailable in this browser.' : 'Reading media duration…'} Estimate assumes one audio channel; multichannel files are billed per channel.`,
          pricingURL: ASSEMBLYAI_PRICING_URL,
        };
      }
      const estimate = audioDuration / 3600 * rate;
      return {
        title: 'Estimated cost',
        amount: `$${estimate.toFixed(estimate < 0.01 ? 4 : 2)} USD`,
        detail: `${modelLabel(settings.model)} · About ${formatDuration(audioDuration)} · ${unitRate} · Estimate assumes one audio channel; multichannel files are billed per channel.`,
        pricingURL: ASSEMBLYAI_PRICING_URL,
      };
    }
    if (!Number.isFinite(audioDuration) || audioDuration <= 0) {
      return { title: 'Estimated cost', amount: '—', detail: durationUnavailable ? 'Duration unavailable in this browser; estimate can’t be calculated.' : 'Reading media duration…' };
    }
    const rate = MODEL_COST_PER_MINUTE[settings.model];
    if (rate == null) return { title: 'Pricing details', amount: 'Token-based', detail: `Billed by input and output tokens · ${formatDuration(audioDuration)}` };
    const estimate = audioDuration / 60 * rate;
    return { title: 'Estimated cost', amount: `$${estimate.toFixed(estimate < 0.01 ? 4 : 2)} USD`, detail: `About ${formatDuration(audioDuration)} · $${rate.toFixed(3)} per audio minute` };
  }, [file, audioDuration, durationUnavailable, settings.model, settings.provider]);
  const selectedJobError = selectedJob ? jobErrorCopy(selectedJob) : { title: '', detail: '' };

  const copyTranscript = async () => {
    try {
      await navigator.clipboard.writeText(selectedJob?.transcript || '');
      setCopyLabel('Copied');
      setTimeout(() => setCopyLabel('Copy text'), 1500);
    } catch {
      setError('Could not copy the transcript.');
    }
  };

  const downloadTranscript = () => {
    const blob = new Blob([selectedJob?.transcript || ''], { type: 'text/plain;charset=utf-8' });
    const link = document.createElement('a');
    link.href = URL.createObjectURL(blob);
    link.download = `${baseName(selectedJob?.name || selectedJob?.filename || 'transcript') || 'transcript'}.txt`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(link.href), 1000);
  };

  return (
    <div className="shell">
      <aside className="rail" aria-label="Transcription navigation">
        <Brand />
        <div className="history-heading"><span>Transcriptions</span>{historyScrollable && <span className="history-hint">Scroll to browse</span>}<span className="jobs-count">{orderedJobs.length}</span></div>
        <div ref={jobsList} className="jobs-list" aria-live="polite">
          {orderedJobs.length ? orderedJobs.map((job) => (
            <article className={`job-row${job.id === selectedJobId ? ' selected' : ''}`} key={job.id}>
              <button className="job-main" type="button" aria-current={job.id === selectedJobId ? 'page' : undefined} onClick={() => selectJob(job.id)}>
                <span className="job-title">{job.name || job.filename || 'Transcription'}</span>
                <span className="job-sub">{job.duration ? `${formatDuration(job.duration)} · ` : ''}{dateLabel(job.created)}</span>
              </button>
              <div className="job-state">
                <Badge className={`job-badge ${job.status || ''}`} color={statusColor(job.status)} variant="light" size="xs">{statusLabel(job.status)}</Badge>
                {['processing', 'queued'].includes(job.status) && <>
                  <span className="job-percent">{job.progress || 0}%</span>
                  <Progress className="job-track" value={Math.max(0, Math.min(100, job.progress || 0))} size={3} radius="xl" color="green" />
                </>}
              </div>
            </article>
          )) : <div className="jobs-empty">No transcriptions yet.</div>}
        </div>
        <div className="rail-bottom"><div className="rail-note">{settings.provider === 'local' ? <>Transcription runs on this computer.<br />No provider API key is needed.</> : <>Your API key is stored locally.<br />Audio is sent to {settings.provider === 'assemblyai' ? 'AssemblyAI' : 'OpenAI'}.</>}</div></div>
      </aside>

      <main className="main">
        <header className="topbar">
          <Button className="top-settings-button" type="button" onClick={openSettings} variant={view === 'settings' ? 'light' : 'default'} color="green" aria-pressed={view === 'settings'}>Settings</Button>
          <Button className="top-new-button" type="button" onClick={openNewTranscription} color="green" leftSection={<PlusIcon />}>New transcription</Button>
        </header>

        <div className="content">
          {view === 'new' ? (
            <section id="new-view" className="new-view">
              <section className="intro"><Title className="intro-title" order={1}>New transcription</Title><Text component="p">Upload an audio or video file to get started.</Text></section>
              <Paper className="workspace-card" component="form" withBorder radius="md" onSubmit={submitTranscription}>
                {settings.provider !== 'local' && !activeAPIKey.trim() && settingsReady && <div className="setup-callout" role="note"><div className="setup-copy"><strong>Connect a provider to start</strong><span>Your key is stored locally. Audio is sent to {settings.provider === 'assemblyai' ? 'AssemblyAI' : 'OpenAI'} for transcription.</span></div><Button className="setup-button" type="button" onClick={openSettings} variant="default" color="green">Add API key</Button></div>}
                <div className="step-head"><div className="step-title">Audio file</div></div>
                <label className={`dropzone${dragging ? ' drag' : ''}${file ? ' hidden' : ''}`} htmlFor="file-input" onDragEnter={(event) => { event.preventDefault(); setDragging(true); }} onDragOver={(event) => { event.preventDefault(); setDragging(true); }} onDragLeave={(event) => { event.preventDefault(); setDragging(false); }} onDrop={(event) => { event.preventDefault(); setDragging(false); chooseFile(event.dataTransfer.files[0]); }}>
                  <input ref={fileInput} id="file-input" type="file" accept="video/*,audio/*,.mp4,.mov,.mkv,.webm,.mp3,.wav,.m4a,.aac,.flac,.ogg" onChange={(event) => chooseFile(event.target.files?.[0])} />
                  <span className="upload-icon"><UploadIcon /></span><span className="drop-title">Drop a file here, or <b>browse</b></span><span className="drop-caption">MP3, WAV, MP4, MOV and more · up to 20 GB</span>
                </label>
                {file && <>
                  <div className="selected-file"><div className="media-icon" aria-hidden="true">FILE</div><div className="media-meta"><div className="media-name">{file.name}</div><div className="media-size">{prettySize(file.size)}</div></div><Button className="icon-button" aria-label="Remove file" type="button" onClick={removeFile} variant="subtle" color="gray">Remove</Button></div>
                  <TextInput className="transcription-name-field" label="Transcription name" description="Defaults to the file name." maxLength={200} value={fileName} onChange={(event) => setFileName(event.currentTarget.value)} placeholder="Name this transcription" size="sm" styles={{ label: { fontSize: 10 }, description: { fontSize: 10 }, input: { height: 38, fontSize: 12 } }} />
                </>}
                <div className="step-head options-head"><div className="step-title">Settings</div><Button className="text-button" type="button" onClick={() => setAdvancedOpen((open) => !open)} variant="subtle" size="xs">{advancedOpen ? 'Hide context' : 'Add context'}</Button></div>
                <div className="controls">
                  <Select className="field" label="Model" description={settings.provider === 'local' ? 'Runs locally on your computer. First start downloads the model.' : settings.provider === 'assemblyai' ? assemblyModelDescription(settings.model) : 'Speaker labels separate voices.'} value={settings.model} onChange={(value) => updateSetting('model', value || DEFAULT_MODEL)} data={availableModels.map(([value, label]) => ({ value, label }))} disabled={uploading} size="sm" styles={{ label: { fontSize: 10 }, description: { fontSize: 10 }, input: { height: 38, fontSize: 12 } }} />
                  <Select className="field" label="Language" value={settings.language || 'auto'} onChange={(value) => updateSetting('language', value && value !== 'auto' ? value : '')} data={LANGUAGES.map(([value, label]) => ({ value, label }))} disabled={uploading} size="sm" styles={{ label: { fontSize: 10 }, input: { height: 38, fontSize: 12 } }} />
                </div>
                {advancedOpen && <div className="advanced"><Textarea className="field" label="Context and vocabulary" rows={3} value={settings.prompt} onChange={(event) => updateSetting('prompt', event.currentTarget.value)} disabled={uploading || isDiarize || settings.provider === 'assemblyai'} placeholder={settings.provider === 'assemblyai' ? 'Unavailable with AssemblyAI.' : isDiarize ? 'Unavailable with speaker diarization.' : 'Names, acronyms, or specialist terms'} description="Help the model recognize specific words." size="sm" styles={{ label: { fontSize: 10 }, description: { fontSize: 10 }, input: { fontSize: 12 } }} /></div>}
                {costInfo && <div className="cost-estimate"><div className="cost-copy"><strong>{costInfo.title}</strong><small>{costInfo.detail}{costInfo.pricingURL && <> · <a className="cost-link" href={costInfo.pricingURL} target="_blank" rel="noreferrer">Pricing details</a></>}</small></div><div className="cost-amount">{costInfo.amount}</div></div>}
                {error && <Alert className="error" color="red" variant="light" role="alert">{error}</Alert>}
                <Button className="primary-button" type="submit" disabled={!readyToSubmit} loading={uploading || validatingKey} fullWidth color="green">{validatingKey ? 'Checking API key…' : uploadProgress !== null ? uploadProgress >= 100 ? 'Finishing upload…' : `Uploading audio… ${uploadProgress}%` : uploading ? 'Preparing transcription…' : 'Start transcription'}</Button>
                {uploadProgress !== null && <div className="upload-progress"><div className="upload-progress-label"><span role="status" aria-live="polite">{uploadProgress >= 100 ? 'Upload sent; waiting for the server' : 'Uploading audio'}</span><span>{uploadProgress}%</span></div><div className="upload-progress-track" role="progressbar" aria-label={uploadProgress >= 100 ? 'Audio uploaded; waiting for server response' : 'Audio upload progress'} aria-valuemin={0} aria-valuemax={100} aria-valuenow={uploadProgress}><span style={{ width: `${uploadProgress}%` }} /></div></div>}
              </Paper>
            </section>
          ) : view === 'settings' ? (
            <section id="settings-view" className="settings-view">
              <section className="intro"><Title className="intro-title" order={1}>Settings</Title><Text component="p">Choose a transcription provider. Local Whisper needs no API key.</Text></section>
              <Paper className="workspace-card settings-card" withBorder radius="md">
                <div className="settings-section-head"><div className="step-title">Provider connection</div><div className={`config-status${configStatus === 'Saved locally' ? ' saved' : configStatus.startsWith('Settings could') ? ' save-error' : ''}`} aria-live="polite">{configStatus}</div></div>
                <Select className="field" label="Transcription provider" value={settings.provider} onChange={(value) => updateSetting('provider', value || 'openai')} data={[{ value: 'openai', label: 'OpenAI' }, { value: 'assemblyai', label: 'AssemblyAI' }, { value: 'local', label: 'Local Whisper (no API fee)' }]} disabled={!settingsReady || uploading || validatingKey} size="sm" styles={{ label: { fontSize: 10 }, input: { height: 38, fontSize: 12 } }} />
                <Text className="settings-description" component="p">{settings.provider === 'local' ? 'Audio is transcribed on this computer with Whisper.cpp. No API key is needed; the model downloads once on first start.' : 'Your key stays in this app’s local database and is used when you start a transcription.'}</Text>
                {settings.provider !== 'local' && <><div className="key-wrap"><PasswordInput className="key-password" aria-label={`${settings.provider === 'assemblyai' ? 'AssemblyAI' : 'OpenAI'} API key`} value={activeAPIKey} onChange={(event) => updateSetting(settings.provider === 'assemblyai' ? 'assemblyai_api_key' : 'api_key', event.currentTarget.value)} disabled={!settingsReady || uploading || validatingKey} placeholder={settings.provider === 'assemblyai' ? '••••••••••••••••••••••••••••••••' : 'sk-••••••••••••••••••••••••••••••••'} autoComplete="off" spellCheck={false} visible={showKey} onVisibilityChange={setShowKey} visibilityToggleButtonProps={{ 'aria-label': 'Toggle API key visibility' }} size="sm" styles={{ root: { flex: 1, minWidth: 0 }, input: { fontSize: 12, height: 40 } }} /> <a href={settings.provider === 'assemblyai' ? 'https://www.assemblyai.com/dashboard/signup' : 'https://platform.openai.com/api-keys'} target="_blank" rel="noreferrer">Get an API key</a></div>
                <div className="key-validation-row"><Text className={`key-validation ${keyCheck.status}`} role="status" aria-live="polite">{keyCheck.message || 'Validate the key before starting a transcription.'}</Text><Button className="validate-key-button" type="button" onClick={checkAPIKey} loading={validatingKey} disabled={!settingsReady || !activeAPIKey.trim() || validatingKey || uploading} variant="default" color="green" size="xs">Validate key</Button></div></>}
                {error && <Alert className="error" color="red" variant="light" role="alert">{error}</Alert>}
              </Paper>
            </section>
          ) : selectedJob ? (
            <section className="job-view" aria-live="polite">
              <div className="detail-heading"><div className="detail-title-wrap"><Title order={1}>{selectedJob.name || selectedJob.filename || 'Transcription'}</Title><div className="detail-subtitle">{selectedJob.filename || selectedJob.name || 'Audio recording'}</div></div><div className="detail-actions"><Badge className="detail-status" color={statusColor(selectedJob.status)} variant="light">{statusLabel(selectedJob.status)}</Badge><Button className="delete-button" type="button" onClick={() => deleteTranscription(selectedJob)} disabled={['processing', 'queued'].includes(selectedJob.status) || Boolean(deletingJobId) || Boolean(retryingJobId)} loading={deletingJobId === selectedJob.id} title={['processing', 'queued'].includes(selectedJob.status) ? 'Available when processing finishes' : 'Delete transcription'} color="red" variant="default" size="xs">Delete</Button></div></div>
              <div className="job-facts"><div className="job-fact"><span>MODEL</span><strong>{modelLabel(selectedJob.model)}</strong></div><div className="job-fact"><span>LANGUAGE</span><strong>{languageLabel(selectedJob.language)}</strong></div><div className="job-fact"><span>DURATION</span><strong>{selectedJob.duration ? formatDuration(selectedJob.duration) : ['processing', 'queued'].includes(selectedJob.status) ? 'Processing…' : '—'}</strong></div><div className="job-fact"><span>CREATED</span><strong>{dateLabel(selectedJob.created)}</strong></div></div>
              {['error', 'interrupted'].includes(selectedJob.status) && <Alert className="error job-error" color="red" variant="light" role="alert"><div className="job-error-content"><div className="job-error-copy"><strong>{selectedJobError.title}</strong>{selectedJobError.detail && <span>{selectedJobError.detail}</span>}</div>{selectedJob.audio_available ? <Button className="retry-button" type="button" onClick={() => retryTranscription(selectedJob)} loading={retryingJobId === selectedJob.id || validatingKey} disabled={!settingsReady || Boolean(retryingJobId) || validatingKey || uploading} color="green" size="xs">{retryingJobId === selectedJob.id && uploadProgress !== null ? uploadProgress >= 100 ? 'Upload sent' : `Uploading ${uploadProgress}%` : 'Try again'}</Button> : <Button className="retry-button" type="button" onClick={chooseAnotherFile} color="green" size="xs">Choose another file</Button>}</div>{retryingJobId === selectedJob.id && uploadProgress !== null && <div className="retry-progress" role="progressbar" aria-label={uploadProgress >= 100 ? 'Audio uploaded; waiting for server response' : 'Retry upload progress'} aria-valuemin={0} aria-valuemax={100} aria-valuenow={uploadProgress}><span style={{ width: `${uploadProgress}%` }} /></div>}</Alert>}
              {['processing', 'queued'].includes(selectedJob.status) && <Paper className="processing" withBorder radius="md"><div className="processing-top"><div><div className="processing-title">{selectedJob.message || 'Transcription is running…'}</div><div className="processing-sub">{selectedJob.total_parts > 1 ? `Part ${selectedJob.current_part || 1} of ${selectedJob.total_parts} · working in the background.` : 'Working in the background. You can start another transcription.'}</div></div><div className="percent">{selectedJob.progress || 0}%</div></div><Progress className="progress-track" value={selectedJob.progress || 0} size={4} radius="xl" color="green" /></Paper>}
              {!(['error', 'interrupted'].includes(selectedJob.status) && !selectedJob.audio_available) && <Paper className="audio-player-section" withBorder radius="md"><div className="audio-player-heading"><Title order={2}>Audio</Title></div>{selectedJob.audio_available ? <audio key={selectedJob.id} controls preload="metadata" src={transcriptionAudioURL(selectedJob.id)} /> : <div className="audio-unavailable">{['processing', 'queued'].includes(selectedJob.status) ? 'Preparing audio. The player will appear when it is ready.' : 'No audio was saved for this job. Start a new transcription and upload the original file to try again.'}</div>}</Paper>}
              {selectedJob.status === 'complete' && selectedJob.transcript && <Paper className="result" withBorder radius="md"><div className="result-heading"><Title order={2}>Transcript</Title><div className="result-actions"><Button className="secondary-button" type="button" onClick={copyTranscript} variant="default" size="xs">{copyLabel}</Button><Button className="secondary-button dark" type="button" onClick={downloadTranscript} color="green" size="xs">Download .txt</Button></div></div><div className="transcript">{selectedJob.transcript}</div></Paper>}
            </section>
          ) : <section className="job-view"><div className="jobs-empty">Choose a transcription from the sidebar, or start a new one.</div></section>}
          {globalError && <Alert className="error" color="red" variant="light" role="alert">{globalError}</Alert>}
        </div>
      </main>
    </div>
  );
}

class AppErrorBoundary extends React.Component<React.PropsWithChildren, { error: Error | null }> {
  constructor(props: React.PropsWithChildren) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (this.state.error) {
      return <main className="app-fatal"><strong>The workspace could not load.</strong><span>{this.state.error.message}</span><button type="button" onClick={() => window.location.reload()}>Reload workspace</button></main>;
    }
    return this.props.children;
  }
}

createRoot(document.getElementById('root')!).render(
  <MantineProvider theme={theme}>
    <AppErrorBoundary><App /></AppErrorBoundary>
  </MantineProvider>,
);
