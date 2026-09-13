<script>
  // One versioned source for every product surface. Text beside the mark stays
  // live (and accessible), rather than being baked into a raster wordmark.
  export let size = 32
  export let alt = ''
</script>

<img
  class="soulacy-brand-mark"
  src="/brand/living-core-blue-v1-128.png"
  {alt}
  aria-hidden={alt ? undefined : 'true'}
  width={size}
  height={size}
  style:width="{size}px"
  style:height="{size}px"
  draggable="false"
/>

<style>
  .soulacy-brand-mark {
    display: block;
    flex: none;
    aspect-ratio: 1;
    object-fit: contain;
    border-radius: 23%;
  }
</style>
