<script setup>
import { reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useClientsStore } from '../stores/clients'

const router = useRouter()
const clients = useClientsStore()

const form = reactive({ hostname: '', sans: [] })

function addSan() {
  form.sans.push('')
}
function removeSan(i) {
  form.sans.splice(i, 1)
}

async function submit() {
  const sans = form.sans.map((s) => s.trim()).filter(Boolean)
  try {
    const result = await clients.enroll(form.hostname, sans)
    router.push(`/clients/${result.hostname}`)
  } catch {
    // error already recorded on clients.error by the store
  }
}
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">New Client</h1>
    <p v-if="clients.error" class="text-red-600 mb-4">{{ clients.error }}</p>
    <form @submit.prevent="submit" class="space-y-6 max-w-2xl">
      <div>
        <label class="block font-medium mb-1">Hostname</label>
        <input name="hostname" v-model="form.hostname" required class="w-full border rounded px-2 py-1" />
      </div>

      <div>
        <label class="block font-medium mb-1">SANs (optional)</label>
        <div v-for="(_, i) in form.sans" :key="i" class="flex gap-2 mb-1">
          <input
            data-test="san-input"
            v-model="form.sans[i]"
            class="flex-1 border rounded px-2 py-1"
          />
          <button type="button" data-test="remove-san" @click="removeSan(i)" class="border rounded px-2">
            Remove
          </button>
        </div>
        <button type="button" data-test="add-san" @click="addSan" class="border rounded px-3 py-1">
          Add SAN
        </button>
      </div>

      <button type="submit" class="bg-blue-600 text-white rounded px-4 py-2">Enroll</button>
    </form>
  </div>
</template>
