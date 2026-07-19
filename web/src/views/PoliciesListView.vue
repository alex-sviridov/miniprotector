<script setup>
import { onMounted } from 'vue'
import { usePoliciesStore } from '../stores/policies'

const policies = usePoliciesStore()

onMounted(() => {
  policies.fetchAll()
})

function confirmDelete(id) {
  if (window.confirm('Delete this policy?')) {
    policies.remove(id)
  }
}
</script>

<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-semibold">Policies</h1>
      <router-link to="/policies/new" class="bg-blue-600 text-white rounded px-3 py-1">
        New Policy
      </router-link>
    </div>
    <p v-if="policies.loading">Loading...</p>
    <p v-else-if="policies.error" class="text-red-600">{{ policies.error }}</p>
    <table v-else class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Name</th>
          <th class="py-2 pr-4">RPO</th>
          <th class="py-2 pr-4">Destination</th>
          <th class="py-2 pr-4"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="policy in policies.list" :key="policy.id" class="border-b hover:bg-gray-50">
          <td class="py-2 pr-4">
            <router-link :to="`/policies/${policy.id}`" class="text-blue-600 hover:underline">
              {{ policy.name }}
            </router-link>
          </td>
          <td class="py-2 pr-4">{{ policy.rpo }}</td>
          <td class="py-2 pr-4">{{ policy.destination }}</td>
          <td class="py-2 pr-4">
            <button @click="confirmDelete(policy.id)" class="border rounded px-2 py-1">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
