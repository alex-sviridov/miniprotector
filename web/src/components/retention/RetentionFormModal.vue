<!-- web/src/components/retention/RetentionFormModal.vue -->
<script setup>
import { reactive, ref, onMounted, onBeforeUnmount } from 'vue'
import RepeatableFieldList from '../ui/RepeatableFieldList.vue'
import BaseButton from '../ui/BaseButton.vue'
import BaseField from '../ui/BaseField.vue'
import BaseInput from '../ui/BaseInput.vue'
import BaseSelect from '../ui/BaseSelect.vue'
import { toFormShape, toPayload, validateRetentionForm } from '../../utils/retentionRule'

const props = defineProps({
  policy: { type: Object, default: null },
  serverError: { type: String, default: '' },
})
const emit = defineEmits(['close', 'save'])

const form = reactive(toFormShape(props.policy))
const errors = ref({})

function close() {
  emit('close')
}

function onKeydown(event) {
  if (event.key === 'Escape') close()
}

onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))

function submit() {
  errors.value = validateRetentionForm(form)
  if (Object.keys(errors.value).length > 0) return
  emit('save', toPayload(form))
}
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-2xl w-full max-h-[90vh] overflow-y-auto">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">{{ policy ? 'Edit Retention Rule' : 'New Retention Rule' }}</h2>
        <BaseButton variant="secondary" data-test="retention-cancel" @click="close">Cancel</BaseButton>
      </div>
      <p v-if="serverError" class="text-red-600 mb-4" data-test="retention-server-error">{{ serverError }}</p>
      <form novalidate @submit.prevent="submit" class="space-y-6">
        <BaseField label="Name" required>
          <BaseInput data-test="retention-name-input" v-model="form.name" />
          <p v-if="errors.name" class="text-red-600 text-sm mt-1" data-test="retention-error-name">{{ errors.name }}</p>
        </BaseField>

        <div>
          <label class="block font-medium mb-1">Applies to hostnames (glob patterns; empty = all clients)</label>
          <RepeatableFieldList :items="form.client_filters.hostnames" add-label="Add Hostname" test-prefix="hostname">
            <template #row="{ index }">
              <input
                data-test="hostname-input"
                v-model="form.client_filters.hostnames[index]"
                class="flex-1 border rounded px-2 py-1"
              />
            </template>
          </RepeatableFieldList>
        </div>

        <div>
          <label class="block font-medium mb-1">…and labels</label>
          <RepeatableFieldList
            :items="form.client_filters.labels"
            :new-item="() => ({ key: '', value: '' })"
            add-label="Add Label"
            test-prefix="label"
          >
            <template #row="{ index }">
              <input
                data-test="label-key-input"
                v-model="form.client_filters.labels[index].key"
                placeholder="key"
                class="flex-1 border rounded px-2 py-1"
              />
              <input
                data-test="label-value-input"
                v-model="form.client_filters.labels[index].value"
                placeholder="value"
                class="flex-1 border rounded px-2 py-1"
              />
            </template>
          </RepeatableFieldList>
        </div>

        <BaseField label="Backup type" required>
          <BaseSelect data-test="retention-backup-type" v-model="form.backup_type">
            <option value="filesystem">Filesystem</option>
          </BaseSelect>
        </BaseField>

        <BaseField label="Path (this directory and everything below it)" required>
          <BaseInput data-test="retention-path-input" v-model="form.path" placeholder="/var/log" />
          <p v-if="errors.path" class="text-red-600 text-sm mt-1" data-test="retention-error-path">{{ errors.path }}</p>
        </BaseField>

        <div>
          <label class="block font-medium mb-1">Only files named (glob patterns, optional)</label>
          <RepeatableFieldList :items="form.include" add-label="Add Pattern" test-prefix="include">
            <template #row="{ index }">
              <input
                data-test="include-input"
                v-model="form.include[index]"
                placeholder="*.log"
                class="flex-1 border rounded px-2 py-1"
              />
            </template>
          </RepeatableFieldList>
          <p v-if="errors.include" class="text-red-600 text-sm mt-1" data-test="retention-error-include">
            {{ errors.include }}
          </p>
        </div>

        <BaseField label="Keep" required>
          <div class="flex items-center gap-3">
            <input
              data-test="retention-keep-days-input"
              v-model="form.keepDays"
              :disabled="form.keepForever"
              inputmode="numeric"
              class="w-24 border rounded px-2 py-1 disabled:bg-gray-100"
            />
            <span class="text-gray-600">days</span>
            <label class="flex items-center gap-1 ml-4">
              <input data-test="retention-keep-forever" type="checkbox" v-model="form.keepForever" />
              Keep forever
            </label>
          </div>
          <p v-if="errors.keepDays" class="text-red-600 text-sm mt-1" data-test="retention-error-keepDays">
            {{ errors.keepDays }}
          </p>
        </BaseField>

        <BaseButton type="submit" variant="primary">
          {{ policy ? 'Save Changes' : 'Create Retention Rule' }}
        </BaseButton>
      </form>
    </div>
  </div>
</template>
