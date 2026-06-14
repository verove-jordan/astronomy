import { ref, onUnmounted } from 'vue'
import { eventsUrl } from '@/services/api'

interface JobEvent {
  job_id: number
  status: string
  progress: number
  step: string
  line?: string
  done?: boolean
}

// useJobStream subscribes to a job's SSE progress stream and exposes reactive progress.
export function useJobStream(jobId: number, onDone?: () => void) {
  const progress = ref(0)
  const step = ref('')
  const status = ref('queued')
  const done = ref(false)

  const source = new EventSource(eventsUrl(jobId))
  source.onmessage = (ev: MessageEvent<string>) => {
    const e = JSON.parse(ev.data) as JobEvent
    progress.value = e.progress
    step.value = e.step
    status.value = e.status
    if (e.done) {
      done.value = true
      source.close()
      onDone?.()
    }
  }
  source.onerror = () => {
    // The browser auto-reconnects; nothing to do.
  }

  onUnmounted(() => source.close())

  return { progress, step, status, done }
}
