import { defineStore } from 'pinia'
import { ref } from 'vue'
import { apiGet, apiPost } from '@/services/api'
import type { Job } from '@/types'

export const useJobsStore = defineStore('jobs', () => {
  const jobs = ref<Job[]>([])
  const current = ref<Job | null>(null)
  const loading = ref(false)
  const error = ref('')

  async function list() {
    loading.value = true
    error.value = ''
    try {
      const data = await apiGet<{ jobs: Job[] }>('/api/jobs')
      jobs.value = data.jobs || []
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  async function get(id: number) {
    loading.value = true
    error.value = ''
    try {
      current.value = await apiGet<Job>(`/api/jobs/${id}`)
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  async function create(path: string, mode: string, format: string): Promise<number> {
    const data = await apiPost<{ id: number }>('/api/jobs', { path, mode, format })
    return data.id
  }

  return { jobs, current, loading, error, list, get, create }
})
