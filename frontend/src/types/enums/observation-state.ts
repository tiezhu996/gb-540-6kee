export type ObservationState = 'accepted' | 'rejected' | 'superseded'

export const observationStateLabel: Record<ObservationState, string> = {
  accepted: '已接受',
  rejected: '已拒绝',
  superseded: '已替代',
}

export const observationStateTone: Record<ObservationState, 'success' | 'danger' | 'warning'> = {
  accepted: 'success',
  rejected: 'danger',
  superseded: 'warning',
}

export function isUsableObservation(state: string): state is ObservationState {
  return state === 'accepted'
}
