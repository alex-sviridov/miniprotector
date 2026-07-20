import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: () => import('./views/HomeView.vue') },
    { path: '/clients', name: 'clients', component: () => import('./views/ClientsListView.vue') },
    { path: '/clients/new', name: 'client-new', component: () => import('./views/ClientFormView.vue') },
    { path: '/clients/:hostname', name: 'client-detail', component: () => import('./views/ClientDetailView.vue') },
    { path: '/catalog', name: 'catalog', component: () => import('./views/CatalogView.vue') },
    { path: '/policies', name: 'policies', component: () => import('./views/PoliciesListView.vue') },
    { path: '/policies/new', name: 'policy-new', component: () => import('./views/PolicyFormView.vue') },
    { path: '/policies/:id', name: 'policy-detail', component: () => import('./views/PolicyDetailView.vue') },
    { path: '/policies/:id/edit', name: 'policy-edit', component: () => import('./views/PolicyFormView.vue') },
    { path: '/jobs', name: 'jobs', component: () => import('./views/JobsListView.vue') },
    { path: '/jobs/:job_id', name: 'job-detail', component: () => import('./views/JobDetailView.vue') },
  ],
})
