<script setup>
import { reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useClientsStore } from '../stores/clients'
import RepeatableFieldList from '../components/ui/RepeatableFieldList.vue'
import BaseButton from '../components/ui/BaseButton.vue'

const router = useRouter()
const clients = useClientsStore()

const form = reactive({ hostname: '', sans: [] })

async function submit() {
  const sans = form.sans.map((s) => s.trim()).filter(Boolean)
  try {
    const result = await clients.enroll(form.hostname, sans)
    router.push({ name: 'client-detail', params: { hostname: result.hostname } })
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
        <RepeatableFieldList :items="form.sans" add-label="Add SAN" test-prefix="san">
          <template #row="{ index }">
            <input data-test="san-input" v-model="form.sans[index]" class="flex-1 border rounded px-2 py-1" />
          </template>
        </RepeatableFieldList>
      </div>

      <BaseButton type="submit" variant="primary">Enroll</BaseButton>
    </form>
  </div>
</template>
