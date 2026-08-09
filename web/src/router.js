import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: () => import('./views/HomeView.vue') },
    { path: '/clients', name: 'clients', component: () => import('./views/ClientsListView.vue') },
    { path: '/clients/new', name: 'client-new', component: () => import('./views/ClientFormView.vue') },
    { path: '/clients/:hostname', name: 'client-detail', component: () => import('./views/ClientDetailView.vue') },
    { path: '/catalog', name: 'catalog', component: () => import('./views/CatalogView.vue') },
    { path: '/restore', name: 'restore', component: () => import('./views/RestoreView.vue') },
    { path: '/policies', name: 'policies', component: () => import('./views/BackupPoliciesView.vue') },
    { path: '/policies/:id', name: 'policy-detail', component: () => import('./views/BackupPolicyView.vue') },
    { path: '/storage', name: 'storage', component: () => import('./views/StorageView.vue') },
    { path: '/storage/:id', name: 'storage-detail', component: () => import('./views/StoragePolicyView.vue') },
    { path: '/jobs', name: 'jobs', component: () => import('./views/JobsListView.vue') },
    { path: '/jobs/:job_id', name: 'job-detail', component: () => import('./views/JobDetailView.vue') },
  ],
})
