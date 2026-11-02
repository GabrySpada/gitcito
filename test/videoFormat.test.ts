import { describe, it, expect } from 'vitest'
import { pickRecordingType, videoFileFor } from '../src/renderer/src/lib/videoFormat'

describe('pickRecordingType', () => {
  it('prefers WebM VP9 when Chromium supports it', () => {
    expect(pickRecordingType(() => true)).toBe('video/webm;codecs=vp9')
  })
  it('falls back to MP4 on WebKit, which has no WebM encoder', () => {
    expect(pickRecordingType((m) => m.startsWith('video/mp4'))).toBe('video/mp4;codecs=avc1')
  })
  it('returns undefined when nothing is supported, leaving the choice to the recorder', () => {
    expect(pickRecordingType(() => false)).toBeUndefined()
  })
})

describe('videoFileFor', () => {
  it('names MP4 recordings .mp4', () => {
    expect(videoFileFor('video/mp4;codecs=avc1')).toEqual({ extension: 'mp4', filterName: 'MP4 video' })
  })
  it('names WebM recordings .webm', () => {
    expect(videoFileFor('video/webm;codecs=vp9')).toEqual({ extension: 'webm', filterName: 'WebM video' })
  })
  it('defaults to WebM for an unknown type, as before', () => {
    expect(videoFileFor(undefined).extension).toBe('webm')
  })
})
