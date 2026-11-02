/**
 * Timelapse recording format. Chromium (Electron) records WebM; WKWebView records MP4 (H.264) and has no
 * WebM encoder. The file extension and the save-dialog filter follow the format actually recorded, so MP4
 * bytes are never saved under a `.webm` name.
 */

/** Preference order: WebM where available (smaller, what earlier versions produced), MP4 otherwise. */
export const RECORDING_TYPES = [
  'video/webm;codecs=vp9',
  'video/webm;codecs=vp8',
  'video/webm',
  'video/mp4;codecs=avc1',
  'video/mp4'
] as const

export function pickRecordingType(isSupported: (mime: string) => boolean): string | undefined {
  return RECORDING_TYPES.find((m) => isSupported(m))
}

export interface VideoFile {
  extension: 'webm' | 'mp4'
  /** Format name for the save dialog's filter — a file-format name, not translated copy. */
  filterName: string
}

/** Extension and save filter for what the recorder produced (`recorder.mimeType` / blob type). */
export function videoFileFor(mime: string | undefined): VideoFile {
  if (mime && /^video\/mp4/i.test(mime)) return { extension: 'mp4', filterName: 'MP4 video' }
  return { extension: 'webm', filterName: 'WebM video' }
}
